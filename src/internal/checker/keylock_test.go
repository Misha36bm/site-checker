package checker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestKeyLocksSerializeOneKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := newKeyLocks()
		var running, peak atomic.Int32
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				unlock, err := locks.Lock(context.Background(), "a")
				if err != nil {
					t.Error(err)
					return
				}
				defer unlock()
				if n := running.Add(1); n > peak.Load() {
					peak.Store(n)
				}
				time.Sleep(time.Second)
				running.Add(-1)
			})
		}
		wg.Wait()
		if peak.Load() != 1 {
			t.Errorf("peak = %d, want 1", peak.Load())
		}
	})
}

func TestKeyLocksDoNotBlockOtherKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := newKeyLocks()
		unlockA, err := locks.Lock(context.Background(), "a")
		if err != nil {
			t.Fatal(err)
		}
		defer unlockA()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		unlockB, err := locks.Lock(ctx, "b")
		if err != nil {
			t.Fatalf("another key waited: %v", err)
		}
		unlockB()
	})
}

func TestKeyLocksWaitCanBeCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := newKeyLocks()
		unlock, err := locks.Lock(context.Background(), "a")
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := locks.Lock(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}

		// The abandoned wait must not break the lock.
		unlock()
		again, err := locks.Lock(context.Background(), "a")
		if err != nil {
			t.Fatal(err)
		}
		again()
	})
}

func TestKeyLocksLeaveNothingBehind(t *testing.T) {
	locks := newKeyLocks()
	for _, key := range []string{"a", "b", "c"} {
		unlock, err := locks.Lock(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	}

	unlock, err := locks.Lock(context.Background(), "d")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locks.Lock(ctx, "d"); err == nil {
		t.Fatal("expected an error")
	}
	unlock()

	if len(locks.locks) != 0 {
		t.Errorf("%d entries left, so memory would grow with every URL", len(locks.locks))
	}
}
