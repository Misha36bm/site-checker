package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/config"
)

type fakeDB struct {
	CheckRepository // not used by these tests
	pingErrs        []error
	pings           int
}

func (f *fakeDB) Ping(context.Context) error {
	f.pings++
	if len(f.pingErrs) == 0 {
		return nil
	}
	err := f.pingErrs[0]
	f.pingErrs = f.pingErrs[1:]
	return err
}
func (f *fakeDB) Migrate(context.Context) error { return nil }
func (f *fakeDB) Close() error                  { return nil }

func noEnv(string) string { return "" }

func TestOpenInitializesOnlySelectedDriver(t *testing.T) {
	var selected, other int
	Register("test-selected", func(config.Getter, PoolConfig) (Database, error) {
		selected++
		return &fakeDB{}, nil
	})
	Register("test-other", func(config.Getter, PoolConfig) (Database, error) {
		other++
		return &fakeDB{}, nil
	})

	if _, err := Open(Config{Driver: "test-selected"}, noEnv); err != nil {
		t.Fatal(err)
	}
	if selected != 1 || other != 0 {
		t.Errorf("selected factory ran %d times, other ran %d times", selected, other)
	}
}

func TestOpenUnknownDriver(t *testing.T) {
	_, err := Open(Config{Driver: "oracle"}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "oracle") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenReportsFactoryError(t *testing.T) {
	boom := errors.New("boom")
	Register("test-failing", func(config.Getter, PoolConfig) (Database, error) { return nil, boom })
	if _, err := Open(Config{Driver: "test-failing"}, noEnv); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	factory := func(config.Getter, PoolConfig) (Database, error) { return &fakeDB{}, nil }
	Register("test-duplicate", factory)
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	Register("test-duplicate", factory)
}

func TestLoadConfig(t *testing.T) {
	values := map[string]string{"DB_DRIVER": "postgres", "DB_MAX_OPEN_CONNS": "7"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != "postgres" || cfg.Pool.MaxOpenConns != 7 || cfg.Pool.MaxIdleConns != 5 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRequiresDriver(t *testing.T) {
	if _, err := LoadConfig(noEnv); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWaitReadyRetriesUntilReachable(t *testing.T) {
	db := &fakeDB{pingErrs: []error{errors.New("not yet")}}
	if err := WaitReady(context.Background(), db, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if db.pings != 2 {
		t.Errorf("pings = %d, want 2", db.pings)
	}
}

func TestWaitReadyGivesUp(t *testing.T) {
	down := errors.New("connection refused")
	db := &fakeDB{pingErrs: []error{down, down, down, down}}
	if err := WaitReady(context.Background(), db, 50*time.Millisecond); !errors.Is(err, down) {
		t.Fatalf("err = %v", err)
	}
}
