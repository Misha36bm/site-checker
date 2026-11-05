//go:build integration

// Package tests holds tests that need real infrastructure. They read the
// POSTGRES_* settings from the environment and run in a temporary database, so
// existing data is not touched. The user must be allowed to create databases.
package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/database/postgres"
)

func TestPostgresMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin, err := postgres.LoadConfig(os.Getenv)
	if err != nil {
		t.Fatalf("POSTGRES_* settings are required: %v", err)
	}
	adminDB, err := sql.Open("pgx", admin.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	name := fmt.Sprintf("site_checker_test_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := adminDB.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	}()

	get := func(key string) string {
		if key == "POSTGRES_DATABASE" {
			return name
		}
		return os.Getenv(key)
	}
	db, err := database.Open(database.Config{
		Driver: postgres.DriverName,
		Pool:   database.PoolConfig{MaxOpenConns: 5, MaxIdleConns: 1},
	}, get)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.WaitReady(ctx, db, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	// Instances starting together must not race on the schema.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.Migrate(ctx)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeated Migrate: %v", err)
	}

	cfg, err := postgres.LoadConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	check, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()

	var status string
	var attempts, versions int
	err = check.QueryRowContext(ctx, `
		INSERT INTO checks (url, url_hash) VALUES ('https://example.com', repeat('a', 64))
		RETURNING status, attempts`).Scan(&status, &attempts)
	if err != nil {
		t.Fatalf("insert into checks: %v", err)
	}
	if status != "pending" || attempts != 0 {
		t.Errorf("defaults: status %q, attempts %d", status, attempts)
	}
	if err := check.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Errorf("schema_migrations has %d rows, want 1", versions)
	}
}
