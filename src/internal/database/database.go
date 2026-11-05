// Package database defines the contracts of the database layer and selects a
// driver at startup. It does not import its drivers: each driver registers
// itself with Register, and cmd/server imports the drivers it ships with.
package database

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/config"
)

const (
	pingTimeout   = 3 * time.Second
	retryInterval = time.Second
)

// Factory creates a driver's Database. It reads the driver's own connection
// settings through get, so settings of unused engines are never required.
type Factory func(get config.Getter, pool PoolConfig) (Database, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a driver available under name. It panics on a duplicate name,
// which can only be a programming error.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic("database: driver registered twice: " + name)
	}
	registry[name] = factory
}

// Open creates the database selected by cfg. It does not wait for the database
// to become reachable; see WaitReady.
func Open(cfg Config, get config.Getter) (Database, error) {
	mu.RLock()
	factory, ok := registry[cfg.Driver]
	available := make([]string, 0, len(registry))
	for name := range registry {
		available = append(available, name)
	}
	mu.RUnlock()

	if !ok {
		slices.Sort(available)
		return nil, fmt.Errorf("unknown DB_DRIVER %q (available: %s)", cfg.Driver, strings.Join(available, ", "))
	}
	db, err := factory(get, cfg.Pool)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", cfg.Driver, err)
	}
	return db, nil
}

// WaitReady pings db until it responds or timeout passes. Databases started by
// Docker Compose or restarted externally may take a while to accept connections.
func WaitReady(ctx context.Context, db Database, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		pingCtx, cancelPing := context.WithTimeout(ctx, pingTimeout)
		err := db.Ping(pingCtx)
		cancelPing()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("database not reachable after %s: %w", timeout, err)
		case <-time.After(retryInterval):
		}
	}
}
