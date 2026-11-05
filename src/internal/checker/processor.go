package checker

import (
	"context"
	"log/slog"

	"github.com/Misha36bm/site-checker/src/internal/metadata"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

// ResultStore persists stage results.
type ResultStore interface {
	SaveHTTPResult(ctx context.Context, lease models.Lease, result models.HTTPResult) error
	SaveMetadataResult(ctx context.Context, lease models.Lease, result models.MetadataResult) error
}

// Processor runs the stages of a check, persisting each result as soon as the
// stage ends so that a later failure cannot lose it.
type Processor struct {
	store ResultStore
	http  *HTTPChecker
}

// NewProcessor returns a Processor.
func NewProcessor(store ResultStore, http *HTTPChecker) *Processor {
	return &Processor{store: store, http: http}
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

	// A metadata problem must not affect the HTTP result, so the check stays
	// completed whatever happens from here on.
	return models.CheckOutcome{
		Status:           models.StatusCompleted,
		MetadataStatus:   p.extractMetadata(ctx, lease, out, log),
		ScreenshotStatus: models.StageSkipped,
	}
}

func (p *Processor) extractMetadata(ctx context.Context, lease models.Lease, out HTTPOutcome, log *slog.Logger) string {
	if out.Body == nil {
		return models.StageSkipped
	}
	found, err := metadata.Extract(out.Body, out.ContentType)
	if err != nil {
		log.Warn("extract metadata", "error", err)
		return models.StageFailed
	}
	result := models.MetadataResult{Title: found.Title, Description: found.Description}
	if err := p.store.SaveMetadataResult(ctx, lease, result); err != nil {
		log.Error("save metadata", "error", err)
		return models.StageFailed
	}
	return models.StageSuccess
}
