// Package models defines the domain types shared across the application.
package models

import "time"

// Check statuses. See docs/SPEC.md section 8.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Stage statuses, used for metadata and screenshot stages.
const (
	StagePending = "pending"
	StageSuccess = "success"
	StageFailed  = "failed"
	StageSkipped = "skipped"
)

// Error codes stored with a check.
const (
	ErrorWorkerAbandoned = "worker_abandoned"
	ErrorInternal        = "internal_error"
)

// RedirectHop is one response in a redirect chain.
type RedirectHop struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	DurationMS int64  `json:"duration_ms"`
}

// Check is a website check and everything known about it so far. Pointer fields
// are nil until the corresponding result is available.
type Check struct {
	ID               int64         `json:"id"`
	URL              string        `json:"url"`
	Status           string        `json:"status"`
	FinalURL         *string       `json:"final_url,omitempty"`
	BrowserFinalURL  *string       `json:"browser_final_url,omitempty"`
	ErrorCode        *string       `json:"error_code,omitempty"`
	ErrorMessage     *string       `json:"error_message,omitempty"`
	HTTPStatusCode   *int          `json:"http_status_code,omitempty"`
	IsAvailable      *bool         `json:"is_available,omitempty"`
	ResponseTimeMS   *int64        `json:"response_time_ms,omitempty"`
	RedirectCount    int           `json:"redirect_count"`
	RedirectChain    []RedirectHop `json:"redirect_chain,omitempty"`
	MetadataStatus   string        `json:"metadata_status"`
	Title            *string       `json:"title,omitempty"`
	Description      *string       `json:"description,omitempty"`
	ScreenshotStatus string        `json:"screenshot_status"`
	ScreenshotError  *string       `json:"screenshot_error,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	StartedAt        *time.Time    `json:"started_at,omitempty"`
	CompletedAt      *time.Time    `json:"completed_at,omitempty"`

	// Internal fields, never returned by the API.
	ScreenshotPath *string    `json:"-"`
	Attempts       int        `json:"-"`
	LockedUntil    *time.Time `json:"-"`
}

// Lease returns the worker's claim on the check.
func (c *Check) Lease() Lease {
	return Lease{CheckID: c.ID, Attempt: c.Attempts}
}

// Lease identifies one worker's claim on a check. Attempt acts as a fencing
// token: once a claim has expired and the check was claimed again, writes made
// under the old lease are rejected.
type Lease struct {
	CheckID int64
	Attempt int
}

// CheckOutcome is the final state of a processed check.
type CheckOutcome struct {
	Status           string
	ErrorCode        string
	ErrorMessage     string
	MetadataStatus   string
	ScreenshotStatus string
}
