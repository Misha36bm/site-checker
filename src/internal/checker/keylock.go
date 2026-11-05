package checker

import (
	"context"
	"sync"
)

// keyLocks is a set of mutexes created on demand, one per key. It serializes work
// within this process only. Several API instances could still capture one URL at
// once, which wastes work but stays correct: files are replaced atomically.
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	held chan struct{} // a one-slot channel, so waiting can be cancelled
	refs int           // holders and waiters; at zero the entry is dropped
}

func newKeyLocks() *keyLocks {
	return &keyLocks{locks: map[string]*keyLock{}}
}

// Lock waits for key and returns the function that releases it, or ctx's error if
// ctx ends first.
func (k *keyLocks) Lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	lock, exists := k.locks[key]
	if !exists {
		lock = &keyLock{held: make(chan struct{}, 1)}
		k.locks[key] = lock
	}
	lock.refs++
	k.mu.Unlock()

	select {
	case lock.held <- struct{}{}:
		return func() {
			<-lock.held
			k.drop(key, lock)
		}, nil
	case <-ctx.Done():
		k.drop(key, lock)
		return nil, ctx.Err()
	}
}

func (k *keyLocks) drop(key string, lock *keyLock) {
	k.mu.Lock()
	defer k.mu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(k.locks, key)
	}
}
