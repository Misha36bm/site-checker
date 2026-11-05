package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

const checkColumns = `id, url, status, final_url, browser_final_url, error_code, error_message,
	http_status_code, is_available, response_time_ms, redirect_count, redirect_chain,
	metadata_status, title, description, screenshot_status, screenshot_error, screenshot_path,
	screenshot_captured_at, attempts, locked_until, created_at, started_at, completed_at`

func scanCheck(row *sql.Row) (*models.Check, error) {
	var (
		c     models.Check
		chain []byte
	)
	err := row.Scan(
		&c.ID, &c.URL, &c.Status, &c.FinalURL, &c.BrowserFinalURL, &c.ErrorCode, &c.ErrorMessage,
		&c.HTTPStatusCode, &c.IsAvailable, &c.ResponseTimeMS, &c.RedirectCount, &chain,
		&c.MetadataStatus, &c.Title, &c.Description, &c.ScreenshotStatus, &c.ScreenshotError, &c.ScreenshotPath,
		&c.ScreenshotCapturedAt, &c.Attempts, &c.LockedUntil, &c.CreatedAt, &c.StartedAt, &c.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if chain != nil {
		if err := json.Unmarshal(chain, &c.RedirectChain); err != nil {
			return nil, fmt.Errorf("decode redirect chain: %w", err)
		}
	}
	c.CreatedAt = c.CreatedAt.UTC()
	for _, t := range []**time.Time{&c.ScreenshotCapturedAt, &c.LockedUntil, &c.StartedAt, &c.CompletedAt} {
		if *t != nil {
			utc := (*t).UTC()
			*t = &utc
		}
	}
	return &c, nil
}

// CreateCheck stores a new pending check.
func (d *DB) CreateCheck(ctx context.Context, url string) (*models.Check, error) {
	row := d.sql.QueryRowContext(ctx,
		`INSERT INTO checks (url, url_hash) VALUES ($1, $2) RETURNING `+checkColumns,
		url, database.URLHash(url))
	return scanCheck(row)
}

// GetCheck returns database.ErrNotFound if the check does not exist.
func (d *DB) GetCheck(ctx context.Context, id int64) (*models.Check, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+checkColumns+` FROM checks WHERE id = $1`, id)
	c, err := scanCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, database.ErrNotFound
	}
	return c, err
}

// CountPendingChecks returns the number of checks waiting for a worker.
func (d *DB) CountPendingChecks(ctx context.Context) (int64, error) {
	var n int64
	err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM checks WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// ClaimCheck claims the oldest pending check. SKIP LOCKED lets concurrent workers
// pass over rows another worker is claiming instead of waiting for them.
func (d *DB) ClaimCheck(ctx context.Context, lockTTL time.Duration) (*models.Check, error) {
	row := d.sql.QueryRowContext(ctx, `
		UPDATE checks SET
			status = 'running',
			attempts = attempts + 1,
			locked_until = now() + make_interval(secs => $1),
			started_at = COALESCE(started_at, now())
		WHERE id = (
			SELECT id FROM checks
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+checkColumns, lockTTL.Seconds())
	c, err := scanCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, database.ErrNoPendingChecks
	}
	return c, err
}

// ExtendLease renews a claim.
func (d *DB) ExtendLease(ctx context.Context, lease models.Lease, lockTTL time.Duration) error {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET locked_until = now() + make_interval(secs => $3)
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt, lockTTL.Seconds())
	return leaseResult(result, err)
}

// ReleaseCheck returns a claimed check to the queue. Decrementing attempts stays
// safe for fencing: an expired lease is recovered without a decrement, so any
// later claim still gets a higher attempt number than the expired lease holds.
func (d *DB) ReleaseCheck(ctx context.Context, lease models.Lease) error {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET status = 'pending', attempts = attempts - 1, locked_until = NULL
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt)
	return leaseResult(result, err)
}

// SaveHTTPResult stores the HTTP stage's result.
func (d *DB) SaveHTTPResult(ctx context.Context, lease models.Lease, r models.HTTPResult) error {
	var chain any // stays NULL when empty
	if len(r.RedirectChain) > 0 {
		encoded, err := json.Marshal(r.RedirectChain)
		if err != nil {
			return fmt.Errorf("encode redirect chain: %w", err)
		}
		chain = string(encoded)
	}
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET
			final_url = NULLIF($3, ''),
			http_status_code = $4,
			is_available = $5,
			response_time_ms = $6,
			redirect_count = $7,
			redirect_chain = $8,
			metadata_status = 'pending',
			title = NULL,
			description = NULL,
			screenshot_status = 'pending',
			screenshot_path = NULL,
			screenshot_error = NULL,
			screenshot_captured_at = NULL,
			browser_final_url = NULL
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt, r.FinalURL, r.StatusCode, r.IsAvailable, r.ResponseTimeMS,
		r.RedirectCount(), chain)
	return leaseResult(result, err)
}

// SaveMetadataResult stores the metadata stage's result.
func (d *DB) SaveMetadataResult(ctx context.Context, lease models.Lease, r models.MetadataResult) error {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET
			metadata_status = 'success',
			title = NULLIF($3, ''),
			description = NULLIF($4, '')
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt, r.Title, r.Description)
	return leaseResult(result, err)
}

// SaveScreenshotResult stores the screenshot stage's result.
func (d *DB) SaveScreenshotResult(ctx context.Context, lease models.Lease, r models.ScreenshotResult) error {
	var capturedAt *time.Time // stays NULL without a screenshot
	if !r.CapturedAt.IsZero() {
		capturedAt = &r.CapturedAt
	}
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET
			screenshot_status = $3,
			screenshot_path = NULLIF($4, ''),
			screenshot_error = NULLIF($5, ''),
			browser_final_url = NULLIF($6, ''),
			screenshot_captured_at = $7
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt, r.Status, r.Path, r.Error, r.BrowserFinalURL, capturedAt)
	return leaseResult(result, err)
}

// FindCapturedCheck returns the latest check that rendered the screenshot at path.
func (d *DB) FindCapturedCheck(ctx context.Context, path string) (*models.Check, error) {
	row := d.sql.QueryRowContext(ctx, `
		SELECT `+checkColumns+` FROM checks
		WHERE screenshot_path = $1 AND browser_final_url IS NOT NULL
		ORDER BY screenshot_captured_at DESC, id DESC
		LIMIT 1`, path)
	c, err := scanCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, database.ErrNotFound
	}
	return c, err
}

// CompleteCheck stores the final state.
func (d *DB) CompleteCheck(ctx context.Context, lease models.Lease, outcome models.CheckOutcome) error {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET
			status = $3,
			error_code = NULLIF($4, ''),
			error_message = NULLIF($5, ''),
			metadata_status = $6,
			screenshot_status = $7,
			locked_until = NULL,
			completed_at = now()
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		lease.CheckID, lease.Attempt, outcome.Status, outcome.ErrorCode, outcome.ErrorMessage,
		outcome.MetadataStatus, outcome.ScreenshotStatus)
	return leaseResult(result, err)
}

func leaseResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return database.ErrLeaseLost
	}
	return nil
}

// RecoverExpiredChecks fails checks that used up their attempts, then requeues
// the remaining expired ones. Each statement is atomic, so concurrent calls from
// several instances are safe.
func (d *DB) RecoverExpiredChecks(ctx context.Context, maxAttempts int) (requeued, abandoned int64, err error) {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE checks SET
			status = 'failed',
			error_code = $2,
			error_message = 'The check was interrupted too many times.',
			metadata_status = CASE WHEN metadata_status = 'pending' THEN 'skipped' ELSE metadata_status END,
			screenshot_status = CASE WHEN screenshot_status = 'pending' THEN 'skipped' ELSE screenshot_status END,
			locked_until = NULL,
			completed_at = now()
		WHERE status = 'running' AND locked_until < now() AND attempts >= $1`,
		maxAttempts, models.ErrorWorkerAbandoned)
	if err != nil {
		return 0, 0, fmt.Errorf("abandon checks: %w", err)
	}
	if abandoned, err = result.RowsAffected(); err != nil {
		return 0, 0, err
	}

	result, err = d.sql.ExecContext(ctx, `
		UPDATE checks SET status = 'pending', locked_until = NULL
		WHERE status = 'running' AND locked_until < now()`)
	if err != nil {
		return 0, abandoned, fmt.Errorf("requeue checks: %w", err)
	}
	requeued, err = result.RowsAffected()
	return requeued, abandoned, err
}
