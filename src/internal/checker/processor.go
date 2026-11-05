package checker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Misha36bm/site-checker/src/internal/browser"
	"github.com/Misha36bm/site-checker/src/internal/metadata"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

// ResultStore persists stage results.
type ResultStore interface {
	SaveHTTPResult(ctx context.Context, lease models.Lease, result models.HTTPResult) error
	SaveMetadataResult(ctx context.Context, lease models.Lease, result models.MetadataResult) error
	SaveScreenshotResult(ctx context.Context, lease models.Lease, result models.ScreenshotResult) error
}

// Screenshotter renders a page. browser.Browser implements it.
type Screenshotter interface {
	Capture(ctx context.Context, pageURL string) (*browser.Capture, error)
}

// ScreenshotStorage keeps screenshot files. browser.Storage implements it.
type ScreenshotStorage interface {
	Save(checkID int64, png []byte) (string, error)
}

// Processor runs the stages of a check, persisting each result as soon as the
// stage ends so that a later failure cannot lose it.
type Processor struct {
	store   ResultStore
	http    *HTTPChecker
	browser Screenshotter
	files   ScreenshotStorage
}

// NewProcessor returns a Processor.
func NewProcessor(store ResultStore, http *HTTPChecker, browser Screenshotter, files ScreenshotStorage) *Processor {
	return &Processor{store: store, http: http, browser: browser, files: files}
}

// Process implements worker.Processor.
func (p *Processor) Process(ctx context.Context, check *models.Check) models.CheckOutcome {
	lease := check.Lease()
	log := slog.With("check_id", check.ID)

	out := p.http.Check(ctx, check.URL)
	if err := p.store.SaveHTTPResult(ctx, lease, out.Result); err != nil {
		log.Error("save HTTP result", "error", err)
		return models.CheckOutcome{
			Status:           models.StatusFailed,
			ErrorCode:        models.ErrorInternal,
			ErrorMessage:     "The check result could not be saved.",
			MetadataStatus:   models.StageSkipped,
			ScreenshotStatus: models.StageSkipped,
		}
	}
	if !out.Final {
		return models.CheckOutcome{
			Status:           models.StatusFailed,
			ErrorCode:        out.ErrorCode,
			ErrorMessage:     out.ErrorMessage,
			MetadataStatus:   models.StageSkipped,
			ScreenshotStatus: models.StageSkipped,
		}
	}

	// From here on a problem must not affect the HTTP result, so the check stays
	// completed whatever happens. Only HTML responses go further.
	if out.Body == nil {
		return models.CheckOutcome{
			Status:           models.StatusCompleted,
			MetadataStatus:   models.StageSkipped,
			ScreenshotStatus: models.StageSkipped,
		}
	}
	metadataStatus, found := p.extractMetadata(ctx, lease, out, log)
	screenshotStatus, capture := p.captureScreenshot(ctx, check, log)
	if capture != nil {
		metadataStatus = p.fillFromDOM(ctx, lease, metadataStatus, found, capture, log)
	}
	return models.CheckOutcome{
		Status:           models.StatusCompleted,
		MetadataStatus:   metadataStatus,
		ScreenshotStatus: screenshotStatus,
	}
}

func (p *Processor) extractMetadata(ctx context.Context, lease models.Lease, out HTTPOutcome, log *slog.Logger) (string, metadata.Metadata) {
	found, err := metadata.Extract(out.Body, out.ContentType)
	if err != nil {
		log.Warn("extract metadata", "error", err)
		return models.StageFailed, metadata.Metadata{}
	}
	result := models.MetadataResult{Title: found.Title, Description: found.Description}
	if err := p.store.SaveMetadataResult(ctx, lease, result); err != nil {
		log.Error("save metadata", "error", err)
		return models.StageFailed, found
	}
	return models.StageSuccess, found
}

func (p *Processor) captureScreenshot(ctx context.Context, check *models.Check, log *slog.Logger) (string, *browser.Capture) {
	lease := check.Lease()
	fail := func(message string) (string, *browser.Capture) {
		result := models.ScreenshotResult{Status: models.StageFailed, Error: message}
		if err := p.store.SaveScreenshotResult(ctx, lease, result); err != nil {
			log.Error("save screenshot failure", "error", err)
		}
		return models.StageFailed, nil
	}

	capture, err := p.browser.Capture(ctx, check.URL)
	if err != nil {
		log.Warn("capture screenshot", "error", err)
		return fail(screenshotMessage(err))
	}
	key, err := p.files.Save(check.ID, capture.PNG)
	if err != nil {
		log.Error("store screenshot", "error", err)
		return fail("The screenshot could not be stored.")
	}
	result := models.ScreenshotResult{
		Status:          models.StageSuccess,
		Path:            key,
		BrowserFinalURL: capture.FinalURL,
	}
	if err := p.store.SaveScreenshotResult(ctx, lease, result); err != nil {
		log.Error("save screenshot", "error", err)
		return models.StageFailed, capture
	}
	return models.StageSuccess, capture
}

// fillFromDOM covers pages that set their title or description with JavaScript,
// which the raw HTML does not show. What the HTML did provide takes priority.
func (p *Processor) fillFromDOM(ctx context.Context, lease models.Lease, status string, found metadata.Metadata, capture *browser.Capture, log *slog.Logger) string {
	merged := found
	if merged.Title == "" {
		merged.Title = metadata.Clean(capture.Title)
	}
	if merged.Description == "" {
		merged.Description = metadata.Clean(capture.Description)
	}
	if merged == found {
		return status
	}
	result := models.MetadataResult{Title: merged.Title, Description: merged.Description}
	if err := p.store.SaveMetadataResult(ctx, lease, result); err != nil {
		log.Error("save metadata from DOM", "error", err)
		return status
	}
	return models.StageSuccess
}

// screenshotMessage returns text safe to show API clients. Browser errors can name
// internal addresses, so they stay in the log.
func screenshotMessage(err error) string {
	switch {
	case errors.Is(err, browser.ErrTimeout):
		return "The page did not finish loading in time."
	case errors.Is(err, browser.ErrRedirectLimit):
		return "The page redirected too many times."
	case errors.Is(err, browser.ErrTooLarge):
		return "The screenshot exceeded the size limit."
	}
	return "The page could not be rendered."
}
