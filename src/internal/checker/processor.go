package checker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Misha36bm/site-checker/src/internal/browser"
	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/metadata"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

// ResultStore persists stage results.
type ResultStore interface {
	FindCapturedCheck(ctx context.Context, path string) (*models.Check, error)
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
	// FindReusable returns a URL's unexpired screenshot of an available page.
	FindReusable(pageURL string) (browser.Stored, bool, error)
	// Save stores a screenshot taken when the site answered with status.
	Save(pageURL string, status int, png []byte) (browser.Stored, error)
}

// Processor runs the stages of a check, persisting each result as soon as the
// stage ends so that a later failure cannot lose it.
type Processor struct {
	store    ResultStore
	http     *HTTPChecker
	browser  Screenshotter
	files    ScreenshotStorage
	captures *keyLocks
}

// NewProcessor returns a Processor.
func NewProcessor(store ResultStore, http *HTTPChecker, browser Screenshotter, files ScreenshotStorage) *Processor {
	return &Processor{store: store, http: http, browser: browser, files: files, captures: newKeyLocks()}
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
	screenshotStatus, capture := p.captureScreenshot(ctx, check, *out.Result.StatusCode, log)
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

func (p *Processor) captureScreenshot(ctx context.Context, check *models.Check, status int, log *slog.Logger) (string, *browser.Capture) {
	lease := check.Lease()
	fail := func(message string) (string, *browser.Capture) {
		result := models.ScreenshotResult{Status: models.StageFailed, Error: message}
		if err := p.store.SaveScreenshotResult(ctx, lease, result); err != nil {
			log.Error("save screenshot failure", "error", err)
		}
		return models.StageFailed, nil
	}

	// An available page already captured is not captured again. An error page always
	// is: a stored screenshot of a working site would misrepresent it. The lock makes
	// concurrent checks of one page wait here and reuse the first one's screenshot.
	if status == http.StatusOK {
		key, err := browser.FileName(check.URL, status)
		if err != nil {
			log.Error("name screenshot", "error", err)
			return fail("The screenshot could not be stored.")
		}
		unlock, err := p.captures.Lock(ctx, key)
		if err != nil {
			return fail("The page could not be rendered.")
		}
		defer unlock()

		stored, exists, err := p.files.FindReusable(check.URL)
		if err != nil {
			log.Error("look up screenshot", "error", err)
			return fail("The screenshot could not be stored.")
		}
		if exists {
			return p.reuseScreenshot(ctx, lease, stored, log)
		}
	}

	capture, err := p.browser.Capture(ctx, check.URL)
	if err != nil {
		log.Warn("capture screenshot", "error", err)
		return fail(screenshotMessage(err))
	}
	stored, err := p.files.Save(check.URL, status, capture.PNG)
	if err != nil {
		log.Error("store screenshot", "error", err)
		return fail("The screenshot could not be stored.")
	}
	result := models.ScreenshotResult{
		Status:          models.StageSuccess,
		Path:            stored.Key,
		BrowserFinalURL: capture.FinalURL,
		CapturedAt:      stored.CapturedAt,
	}
	if err := p.store.SaveScreenshotResult(ctx, lease, result); err != nil {
		log.Error("save screenshot", "error", err)
		return models.StageFailed, capture
	}
	return models.StageSuccess, capture
}

// reuseScreenshot records a stored screenshot for this check. Nothing is rendered, so
// what the browser saw comes from the check that took the screenshot, if it is still
// there. Without it, the screenshot is still valid and those details stay empty.
func (p *Processor) reuseScreenshot(ctx context.Context, lease models.Lease, stored browser.Stored, log *slog.Logger) (string, *browser.Capture) {
	result := models.ScreenshotResult{Status: models.StageSuccess, Path: stored.Key, CapturedAt: stored.CapturedAt}

	var rendered *browser.Capture
	captured, err := p.store.FindCapturedCheck(ctx, stored.Key)
	switch {
	case err == nil:
		result.BrowserFinalURL = deref(captured.BrowserFinalURL)
		rendered = &browser.Capture{Title: deref(captured.Title), Description: deref(captured.Description)}
	case !errors.Is(err, database.ErrNotFound):
		log.Warn("find check that captured screenshot", "error", err)
	}

	if err := p.store.SaveScreenshotResult(ctx, lease, result); err != nil {
		log.Error("save reused screenshot", "error", err)
		return models.StageFailed, rendered
	}
	return models.StageSuccess, rendered
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// fillFromDOM covers pages that set their title or description with JavaScript,
// which the raw HTML does not show. capture is this check's render or, for a reused
// screenshot, the one that took it. What the HTML did provide takes priority.
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
