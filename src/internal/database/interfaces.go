package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/models"
)

var (
	// ErrNotFound means the requested record does not exist.
	ErrNotFound = errors.New("record not found")
	// ErrNoPendingChecks means there is no check to claim.
	ErrNoPendingChecks = errors.New("no pending checks")
	// ErrLeaseLost means the caller's claim on a check is no longer valid.
	ErrLeaseLost = errors.New("lease lost")
)

// Database is the contract every database driver implements.
type Database interface {
	CheckRepository

	// Ping verifies the database is reachable.
	Ping(ctx context.Context) error
	// Migrate applies pending schema migrations. It is safe to call repeatedly
	// and from several processes at once.
	Migrate(ctx context.Context) error
	// Close releases all connections.
	Close() error
}

// CheckRepository stores checks and acts as the work queue.
type CheckRepository interface {
	// CreateCheck stores a new pending check.
	CreateCheck(ctx context.Context, url string) (*models.Check, error)
	// GetCheck returns ErrNotFound if the check does not exist.
	GetCheck(ctx context.Context, id int64) (*models.Check, error)
	// CountPendingChecks returns the number of checks waiting for a worker.
	CountPendingChecks(ctx context.Context) (int64, error)

	// ClaimCheck atomically claims the oldest pending check for lockTTL and
	// counts the attempt. It never returns the same claim to two callers, and
	// returns ErrNoPendingChecks when nothing is waiting.
	ClaimCheck(ctx context.Context, lockTTL time.Duration) (*models.Check, error)
	// ExtendLease renews a claim. It returns ErrLeaseLost if the claim is gone.
	ExtendLease(ctx context.Context, lease models.Lease, lockTTL time.Duration) error
	// ReleaseCheck returns a claimed check to the queue without consuming an
	// attempt, for work interrupted by a graceful shutdown. It returns
	// ErrLeaseLost if the claim is gone.
	ReleaseCheck(ctx context.Context, lease models.Lease) error
	// SaveHTTPResult stores the HTTP stage's result and clears later stages' results
	// left by an earlier, interrupted attempt. It returns ErrLeaseLost if the claim is gone.
	SaveHTTPResult(ctx context.Context, lease models.Lease, result models.HTTPResult) error
	// SaveMetadataResult stores the metadata stage's result. It returns ErrLeaseLost
	// if the claim is gone.
	SaveMetadataResult(ctx context.Context, lease models.Lease, result models.MetadataResult) error
	// SaveScreenshotResult stores the screenshot stage's result. It returns
	// ErrLeaseLost if the claim is gone.
	SaveScreenshotResult(ctx context.Context, lease models.Lease, result models.ScreenshotResult) error
	// CompleteCheck stores the final state. It returns ErrLeaseLost if the claim
	// is gone.
	CompleteCheck(ctx context.Context, lease models.Lease, outcome models.CheckOutcome) error
	// RecoverExpiredChecks handles claims whose worker stopped renewing them.
	// Checks that used up maxAttempts fail with models.ErrorWorkerAbandoned, and
	// the others return to the queue.
	RecoverExpiredChecks(ctx context.Context, maxAttempts int) (requeued, abandoned int64, err error)
}

// URLHash returns the value stored in checks.url_hash. URLs are too long to index
// directly in every engine, so drivers index and filter on this instead.
func URLHash(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}
