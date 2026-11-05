package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/config"
	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

var testConfig = config.Worker{
	Concurrency:     2,
	PollInterval:    10 * time.Millisecond,
	LockTTL:         30 * time.Second,
	MaxAttempts:     3,
	ShutdownTimeout: 10 * time.Second,
}

// memStore is an in-memory queue. The contract tests cover the real drivers.
type memStore struct {
	mu          sync.Mutex
	queue       []*models.Check
	completed   map[int64]models.CheckOutcome
	released    []int64
	extends     int
	leaseLost   bool
	recoverRuns int
}

func newStore(ids ...int64) *memStore {
	s := &memStore{completed: map[int64]models.CheckOutcome{}}
	for _, id := range ids {
		s.queue = append(s.queue, &models.Check{ID: id, Attempts: 1})
	}
	return s
}

func (s *memStore) ClaimCheck(context.Context, time.Duration) (*models.Check, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil, database.ErrNoPendingChecks
	}
	check := s.queue[0]
	s.queue = s.queue[1:]
	return check, nil
}

func (s *memStore) ExtendLease(context.Context, models.Lease, time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extends++
	if s.leaseLost {
		return database.ErrLeaseLost
	}
	return nil
}

func (s *memStore) ReleaseCheck(_ context.Context, lease models.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, lease.CheckID)
	return nil
}

func (s *memStore) CompleteCheck(_ context.Context, lease models.Lease, outcome models.CheckOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed[lease.CheckID] = outcome
	return nil
}

func (s *memStore) RecoverExpiredChecks(context.Context, int) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverRuns++
	return 0, 0, nil
}

type processorFunc func(context.Context, *models.Check) models.CheckOutcome

func (f processorFunc) Process(ctx context.Context, c *models.Check) models.CheckOutcome {
	return f(ctx, c)
}

var done = models.CheckOutcome{Status: models.StatusCompleted}

func TestPoolProcessesAllChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1, 2, 3, 4, 5)
		ctx, cancel := context.WithCancel(context.Background())

		finished := make(chan struct{})
		go func() {
			NewPool(store, processorFunc(func(context.Context, *models.Check) models.CheckOutcome { return done }), testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(time.Second)
		cancel()
		<-finished

		if len(store.completed) != 5 {
			t.Errorf("completed %d checks, want 5", len(store.completed))
		}
		if store.recoverRuns == 0 {
			t.Error("expired checks were never recovered")
		}
	})
}

func TestPoolRespectsConcurrencyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1, 2, 3, 4, 5, 6)
		var running, peak atomic.Int32
		processor := processorFunc(func(context.Context, *models.Check) models.CheckOutcome {
			n := running.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			running.Add(-1)
			return done
		})

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(time.Second)
		cancel()
		<-finished

		if got := peak.Load(); got != int32(testConfig.Concurrency) {
			t.Errorf("peak concurrency = %d, want %d", got, testConfig.Concurrency)
		}
	})
}

func TestShutdownLetsCheckInProgressFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1)
		processor := processorFunc(func(context.Context, *models.Check) models.CheckOutcome {
			time.Sleep(testConfig.ShutdownTimeout / 2)
			return done
		})

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(time.Second)
		cancel()
		<-finished

		if _, ok := store.completed[1]; !ok {
			t.Error("check was not completed")
		}
		if len(store.released) != 0 {
			t.Errorf("released = %v", store.released)
		}
	})
}

func TestShutdownTimeoutReturnsCheckToQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1)
		processor := processorFunc(func(ctx context.Context, _ *models.Check) models.CheckOutcome {
			<-ctx.Done()
			return models.CheckOutcome{Status: models.StatusFailed}
		})

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(time.Second)
		cancel()
		<-finished

		if len(store.released) != 1 || store.released[0] != 1 {
			t.Errorf("released = %v", store.released)
		}
		if len(store.completed) != 0 {
			t.Errorf("an interrupted check was stored as finished: %v", store.completed)
		}
	})
}

func TestHeartbeatKeepsLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1)
		processor := processorFunc(func(context.Context, *models.Check) models.CheckOutcome {
			time.Sleep(testConfig.LockTTL * 2)
			return done
		})

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(testConfig.LockTTL*2 + time.Second)
		cancel()
		<-finished

		if store.extends < 5 {
			t.Errorf("lease extended %d times", store.extends)
		}
		if _, ok := store.completed[1]; !ok {
			t.Error("check was not completed")
		}
	})
}

func TestLostLeaseCancelsWorkAndDiscardsResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1)
		store.leaseLost = true
		var cancelled atomic.Bool
		processor := processorFunc(func(ctx context.Context, _ *models.Check) models.CheckOutcome {
			<-ctx.Done()
			cancelled.Store(true)
			return done
		})

		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, testConfig).Run(ctx)
			close(finished)
		}()

		time.Sleep(testConfig.LockTTL)
		if !cancelled.Load() {
			t.Error("work was not cancelled")
		}
		cancel()
		<-finished

		if len(store.completed) != 0 || len(store.released) != 0 {
			t.Errorf("completed = %v, released = %v", store.completed, store.released)
		}
	})
}

func TestProcessorPanicFailsCheckAndWorkerSurvives(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newStore(1, 2)
		processor := processorFunc(func(_ context.Context, c *models.Check) models.CheckOutcome {
			if c.ID == 1 {
				panic("boom")
			}
			return done
		})

		cfg := testConfig
		cfg.Concurrency = 1
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			NewPool(store, processor, cfg).Run(ctx)
			close(finished)
		}()

		time.Sleep(time.Second)
		cancel()
		<-finished

		if got := store.completed[1]; got.Status != models.StatusFailed || got.ErrorCode != models.ErrorInternal {
			t.Errorf("check 1 outcome = %+v", got)
		}
		if got := store.completed[2]; got.Status != models.StatusCompleted {
			t.Errorf("check 2 outcome = %+v", got)
		}
	})
}
