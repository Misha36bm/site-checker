//go:build integration

package tests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

const (
	longTTL  = time.Minute
	shortTTL = 10 * time.Millisecond
)

// runRepositoryContract is the behavior every database driver must provide. A new
// driver calls it with a function returning an empty, migrated database.
func runRepositoryContract(t *testing.T, open func(t *testing.T) database.Database) {
	ctx := context.Background()

	create := func(t *testing.T, db database.Database, url string) *models.Check {
		t.Helper()
		check, err := db.CreateCheck(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		return check
	}
	claim := func(t *testing.T, db database.Database, ttl time.Duration) *models.Check {
		t.Helper()
		check, err := db.ClaimCheck(ctx, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return check
	}
	get := func(t *testing.T, db database.Database, id int64) *models.Check {
		t.Helper()
		check, err := db.GetCheck(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return check
	}
	recoverExpired := func(t *testing.T, db database.Database, maxAttempts int) (int64, int64) {
		t.Helper()
		time.Sleep(3 * shortTTL)
		requeued, abandoned, err := db.RecoverExpiredChecks(ctx, maxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		return requeued, abandoned
	}
	wantLeaseLost := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, database.ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
	}
	finished := models.CheckOutcome{
		Status:           models.StatusCompleted,
		MetadataStatus:   models.StageSkipped,
		ScreenshotStatus: models.StageSkipped,
	}

	t.Run("create and get", func(t *testing.T) {
		db := open(t)
		url := "https://example.com/páth?q=ключ"
		created := create(t, db, url)
		if created.ID == 0 || created.URL != url || created.Status != models.StatusPending {
			t.Errorf("created = %+v", created)
		}
		if created.MetadataStatus != models.StagePending || created.ScreenshotStatus != models.StagePending {
			t.Errorf("stage statuses = %q, %q", created.MetadataStatus, created.ScreenshotStatus)
		}
		if created.CreatedAt.IsZero() || created.CreatedAt.Location() != time.UTC {
			t.Errorf("created_at = %v", created.CreatedAt)
		}
		got := get(t, db, created.ID)
		if got.URL != url || got.IsAvailable != nil || got.CompletedAt != nil || got.RedirectChain != nil {
			t.Errorf("got = %+v", got)
		}
	})

	t.Run("get missing check", func(t *testing.T) {
		if _, err := open(t).GetCheck(ctx, 999999); !errors.Is(err, database.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("count pending", func(t *testing.T) {
		db := open(t)
		create(t, db, "https://a.example")
		create(t, db, "https://b.example")
		claim(t, db, longTTL)
		n, err := db.CountPendingChecks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("pending = %d, want 1", n)
		}
	})

	t.Run("claim takes oldest and counts the attempt", func(t *testing.T) {
		db := open(t)
		first := create(t, db, "https://first.example")
		create(t, db, "https://second.example")
		got := claim(t, db, longTTL)
		if got.ID != first.ID || got.Status != models.StatusRunning || got.Attempts != 1 {
			t.Errorf("claimed = %+v", got)
		}
		if got.StartedAt == nil || got.LockedUntil == nil || !got.LockedUntil.After(time.Now()) {
			t.Errorf("started_at = %v, locked_until = %v", got.StartedAt, got.LockedUntil)
		}
	})

	t.Run("claim on empty queue", func(t *testing.T) {
		if _, err := open(t).ClaimCheck(ctx, longTTL); !errors.Is(err, database.ErrNoPendingChecks) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("concurrent claims never overlap", func(t *testing.T) {
		db := open(t)
		const total = 30
		for range total {
			create(t, db, "https://example.com")
		}
		var (
			mu     sync.Mutex
			seen   = map[int64]int{}
			wg     sync.WaitGroup
			failed error
		)
		for range 8 {
			wg.Go(func() {
				for {
					check, err := db.ClaimCheck(ctx, longTTL)
					if errors.Is(err, database.ErrNoPendingChecks) {
						return
					}
					mu.Lock()
					if err != nil {
						failed = err
						mu.Unlock()
						return
					}
					seen[check.ID]++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if failed != nil {
			t.Fatal(failed)
		}
		if len(seen) != total {
			t.Errorf("claimed %d distinct checks, want %d", len(seen), total)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("check %d claimed %d times", id, n)
			}
		}
	})

	t.Run("complete stores outcome", func(t *testing.T) {
		db := open(t)
		check := create(t, db, "https://example.com")
		lease := claim(t, db, longTTL).Lease()
		outcome := models.CheckOutcome{
			Status:           models.StatusFailed,
			ErrorCode:        models.ErrorInternal,
			ErrorMessage:     "details",
			MetadataStatus:   models.StageSkipped,
			ScreenshotStatus: models.StageFailed,
		}
		if err := db.CompleteCheck(ctx, lease, outcome); err != nil {
			t.Fatal(err)
		}
		got := get(t, db, check.ID)
		if got.Status != models.StatusFailed || got.MetadataStatus != models.StageSkipped || got.ScreenshotStatus != models.StageFailed {
			t.Errorf("got = %+v", got)
		}
		if got.ErrorCode == nil || *got.ErrorCode != models.ErrorInternal || got.ErrorMessage == nil || *got.ErrorMessage != "details" {
			t.Errorf("error = %v, %v", got.ErrorCode, got.ErrorMessage)
		}
		if got.CompletedAt == nil || got.LockedUntil != nil {
			t.Errorf("completed_at = %v, locked_until = %v", got.CompletedAt, got.LockedUntil)
		}
		wantLeaseLost(t, db.CompleteCheck(ctx, lease, outcome))
	})

	t.Run("complete without error leaves error fields empty", func(t *testing.T) {
		db := open(t)
		check := create(t, db, "https://example.com")
		if err := db.CompleteCheck(ctx, claim(t, db, longTTL).Lease(), finished); err != nil {
			t.Fatal(err)
		}
		got := get(t, db, check.ID)
		if got.ErrorCode != nil || got.ErrorMessage != nil {
			t.Errorf("error = %v, %v", got.ErrorCode, got.ErrorMessage)
		}
	})

	t.Run("extend moves the lock forward", func(t *testing.T) {
		db := open(t)
		create(t, db, "https://example.com")
		check := claim(t, db, time.Second)
		if err := db.ExtendLease(ctx, check.Lease(), longTTL); err != nil {
			t.Fatal(err)
		}
		got := get(t, db, check.ID)
		if !got.LockedUntil.After(*check.LockedUntil) {
			t.Errorf("locked_until %v is not after %v", got.LockedUntil, check.LockedUntil)
		}
	})

	t.Run("release does not consume an attempt", func(t *testing.T) {
		db := open(t)
		check := create(t, db, "https://example.com")
		lease := claim(t, db, longTTL).Lease()
		if err := db.ReleaseCheck(ctx, lease); err != nil {
			t.Fatal(err)
		}
		got := get(t, db, check.ID)
		if got.Status != models.StatusPending || got.Attempts != 0 || got.LockedUntil != nil {
			t.Errorf("got = %+v", got)
		}
		wantLeaseLost(t, db.ReleaseCheck(ctx, lease))
	})

	t.Run("recovery leaves valid claims alone", func(t *testing.T) {
		db := open(t)
		create(t, db, "https://example.com")
		check := claim(t, db, longTTL)
		requeued, abandoned := recoverExpired(t, db, 3)
		if requeued != 0 || abandoned != 0 {
			t.Errorf("requeued = %d, abandoned = %d", requeued, abandoned)
		}
		if got := get(t, db, check.ID); got.Status != models.StatusRunning {
			t.Errorf("status = %q", got.Status)
		}
	})

	t.Run("expired lease is fenced off", func(t *testing.T) {
		db := open(t)
		create(t, db, "https://example.com")
		stale := claim(t, db, shortTTL).Lease()

		if requeued, _ := recoverExpired(t, db, 3); requeued != 1 {
			t.Fatalf("requeued = %d, want 1", requeued)
		}
		wantLeaseLost(t, db.ExtendLease(ctx, stale, longTTL))
		wantLeaseLost(t, db.CompleteCheck(ctx, stale, finished))

		current := claim(t, db, longTTL)
		if current.Attempts != 2 {
			t.Errorf("attempts = %d, want 2", current.Attempts)
		}
		wantLeaseLost(t, db.CompleteCheck(ctx, stale, finished))
		wantLeaseLost(t, db.ReleaseCheck(ctx, stale))
		if err := db.CompleteCheck(ctx, current.Lease(), finished); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fencing survives a release after recovery", func(t *testing.T) {
		db := open(t)
		create(t, db, "https://example.com")
		stale := claim(t, db, shortTTL).Lease()
		recoverExpired(t, db, 3)

		second := claim(t, db, longTTL)
		if err := db.ReleaseCheck(ctx, second.Lease()); err != nil {
			t.Fatal(err)
		}
		third := claim(t, db, longTTL)
		if third.Attempts <= stale.Attempt {
			t.Fatalf("attempts = %d did not move past the stale lease's %d", third.Attempts, stale.Attempt)
		}
		wantLeaseLost(t, db.CompleteCheck(ctx, stale, finished))
	})

	t.Run("recovery abandons after max attempts", func(t *testing.T) {
		db := open(t)
		check := create(t, db, "https://example.com")
		for attempt := 1; attempt <= 2; attempt++ {
			claim(t, db, shortTTL)
			requeued, abandoned := recoverExpired(t, db, 2)
			wantRequeued, wantAbandoned := int64(1), int64(0)
			if attempt == 2 {
				wantRequeued, wantAbandoned = 0, 1
			}
			if requeued != wantRequeued || abandoned != wantAbandoned {
				t.Fatalf("attempt %d: requeued = %d, abandoned = %d", attempt, requeued, abandoned)
			}
		}
		got := get(t, db, check.ID)
		if got.Status != models.StatusFailed || got.ErrorCode == nil || *got.ErrorCode != models.ErrorWorkerAbandoned {
			t.Errorf("got = %+v", got)
		}
		if got.MetadataStatus != models.StageSkipped || got.ScreenshotStatus != models.StageSkipped {
			t.Errorf("stage statuses = %q, %q", got.MetadataStatus, got.ScreenshotStatus)
		}
		if got.CompletedAt == nil {
			t.Error("completed_at is empty")
		}
		if _, err := db.ClaimCheck(ctx, longTTL); !errors.Is(err, database.ErrNoPendingChecks) {
			t.Errorf("abandoned check was claimed again: %v", err)
		}
	})
}
