// Package worker processes queued checks in the background.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/config"
	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

// storeTimeout bounds queue writes, which use their own context so they still
// happen during shutdown.
const storeTimeout = 5 * time.Second

var errLeaseLost = errors.New("lease lost")

// Store is the part of the repository the pool needs.
type Store interface {
	ClaimCheck(ctx context.Context, lockTTL time.Duration) (*models.Check, error)
	ExtendLease(ctx context.Context, lease models.Lease, lockTTL time.Duration) error
	ReleaseCheck(ctx context.Context, lease models.Lease) error
	CompleteCheck(ctx context.Context, lease models.Lease, outcome models.CheckOutcome) error
	RecoverExpiredChecks(ctx context.Context, maxAttempts int) (requeued, abandoned int64, err error)
}

// Processor does the work of one check. It must stop soon after ctx is cancelled.
type Processor interface {
	Process(ctx context.Context, check *models.Check) models.CheckOutcome
}

// Pool runs a fixed number of workers.
type Pool struct {
	store     Store
	processor Processor
	cfg       config.Worker
}

// NewPool returns a Pool.
func NewPool(store Store, processor Processor, cfg config.Worker) *Pool {
	return &Pool{store: store, processor: processor, cfg: cfg}
}

// Run processes checks until ctx is cancelled, then gives in-progress checks
// ShutdownTimeout to finish. Whatever is still running after that is cancelled
// and returned to the queue. Run returns when all workers have stopped.
func (p *Pool) Run(ctx context.Context) {
	hardStop, cancelHardStop := context.WithCancel(context.Background())
	defer cancelHardStop()

	var wg sync.WaitGroup
	for range p.cfg.Concurrency {
		wg.Go(func() { p.work(ctx, hardStop) })
	}
	wg.Go(func() { p.recoverLoop(ctx) })

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()

	<-ctx.Done()
	select {
	case <-stopped:
	case <-time.After(p.cfg.ShutdownTimeout):
		slog.Warn("worker shutdown timeout reached, interrupting checks in progress")
		cancelHardStop()
		<-stopped
	}
}

func (p *Pool) work(ctx, hardStop context.Context) {
	for ctx.Err() == nil {
		check, err := p.store.ClaimCheck(ctx, p.cfg.LockTTL)
		switch {
		case err == nil:
			p.process(hardStop, check)
		case errors.Is(err, database.ErrNoPendingChecks):
			sleep(ctx, p.cfg.PollInterval)
		case ctx.Err() != nil:
			return
		default:
			slog.Error("claim check", "error", err)
			sleep(ctx, p.cfg.PollInterval)
		}
	}
}

func (p *Pool) process(hardStop context.Context, check *models.Check) {
	lease := check.Lease()
	log := slog.With("check_id", check.ID, "attempt", check.Attempts)

	ctx, cancel := context.WithCancelCause(hardStop)
	defer cancel(nil)

	var heartbeat sync.WaitGroup
	stopHeartbeat := make(chan struct{})
	heartbeat.Go(func() {
		p.keepLease(lease, stopHeartbeat, func() { cancel(errLeaseLost) }, log)
	})

	outcome := runProcessor(ctx, p.processor, check, log)

	close(stopHeartbeat)
	heartbeat.Wait()

	storeCtx, cancelStore := context.WithTimeout(context.Background(), storeTimeout)
	defer cancelStore()

	switch {
	case errors.Is(context.Cause(ctx), errLeaseLost):
		log.Warn("lease lost, discarding result")
	case ctx.Err() != nil:
		if err := p.store.ReleaseCheck(storeCtx, lease); err != nil {
			log.Error("release check", "error", err)
			return
		}
		log.Info("check returned to the queue")
	default:
		if err := p.store.CompleteCheck(storeCtx, lease, outcome); err != nil {
			log.Error("complete check", "error", err)
			return
		}
		log.Info("check finished", "status", outcome.Status)
	}
}

// runProcessor turns a panic into a failed check, so one bad check cannot take
// the application down.
func runProcessor(ctx context.Context, processor Processor, check *models.Check, log *slog.Logger) (outcome models.CheckOutcome) {
	defer func() {
		if v := recover(); v != nil {
			log.Error("processor panic", "panic", v, "stack", string(debug.Stack()))
			outcome = models.CheckOutcome{
				Status:           models.StatusFailed,
				ErrorCode:        models.ErrorInternal,
				ErrorMessage:     "The check could not be processed.",
				MetadataStatus:   models.StageSkipped,
				ScreenshotStatus: models.StageSkipped,
			}
		}
	}()
	return processor.Process(ctx, check)
}

// keepLease renews the claim until stop is closed. A temporary database failure
// is only logged, because the lease may still be valid; onLost runs once the
// repository confirms it is gone.
func (p *Pool) keepLease(lease models.Lease, stop <-chan struct{}, onLost func(), log *slog.Logger) {
	ticker := time.NewTicker(p.cfg.LockTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
			err := p.store.ExtendLease(ctx, lease, p.cfg.LockTTL)
			cancel()
			if errors.Is(err, database.ErrLeaseLost) {
				onLost()
				return
			}
			if err != nil {
				log.Error("extend lease", "error", err)
			}
		}
	}
}

// recoverLoop runs immediately, to pick up checks left by a crash, and then at
// regular intervals. Any number of instances can run it at once.
func (p *Pool) recoverLoop(ctx context.Context) {
	for ctx.Err() == nil {
		requeued, abandoned, err := p.store.RecoverExpiredChecks(ctx, p.cfg.MaxAttempts)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.Error("recover expired checks", "error", err)
		case requeued > 0 || abandoned > 0:
			slog.Warn("recovered expired checks", "requeued", requeued, "abandoned", abandoned)
		}
		sleep(ctx, p.cfg.LockTTL)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
