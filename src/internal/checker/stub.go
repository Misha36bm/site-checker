package checker

import (
	"context"

	"github.com/Misha36bm/site-checker/src/internal/models"
)

// StubProcessor completes checks without contacting the site, so the queue can be
// exercised end to end. Phase 3 replaces it with the HTTP checker. All results
// stay empty and is_available stays unset.
type StubProcessor struct{}

// Process implements worker.Processor.
func (StubProcessor) Process(context.Context, *models.Check) models.CheckOutcome {
	return models.CheckOutcome{
		Status:           models.StatusCompleted,
		MetadataStatus:   models.StageSkipped,
		ScreenshotStatus: models.StageSkipped,
	}
}
