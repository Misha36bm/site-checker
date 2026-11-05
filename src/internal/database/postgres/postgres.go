// Package postgres implements the database contracts for PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"slices"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/Misha36bm/site-checker/src/internal/config"
	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/migrations"
)

// DriverName is the DB_DRIVER value that selects this driver.
const DriverName = "postgres"

// migrationLockID identifies the advisory lock serializing schema migrations.
const migrationLockID = 7362519048

func init() {
	database.Register(DriverName, open)
}

// Config holds PostgreSQL connection parameters.
type Config struct {
	Host     string
	Port     string
	Database string
	Username string
	Password string
	SSLMode  string
}

// LoadConfig reads the POSTGRES_* settings.
func LoadConfig(get config.Getter) (Config, error) {
	cfg := Config{
		Host:     get("POSTGRES_HOST"),
		Port:     config.String(get, "POSTGRES_PORT", "5432"),
		Database: get("POSTGRES_DATABASE"),
		Username: get("POSTGRES_USERNAME"),
		Password: get("POSTGRES_PASSWORD"),
		SSLMode:  config.String(get, "POSTGRES_SSLMODE", "disable"),
	}
	var missing []string
	for _, setting := range []struct{ key, value string }{
		{"POSTGRES_HOST", cfg.Host},
		{"POSTGRES_DATABASE", cfg.Database},
		{"POSTGRES_USERNAME", cfg.Username},
		{"POSTGRES_PASSWORD", cfg.Password},
	} {
		if setting.value == "" {
			missing = append(missing, setting.key)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
	}
	valid := []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
	if !slices.Contains(valid, cfg.SSLMode) {
		return Config{}, fmt.Errorf("POSTGRES_SSLMODE: %q is not one of %s", cfg.SSLMode, strings.Join(valid, ", "))
	}
	return cfg, nil
}

// DSN returns the connection URL. It contains the password, so it must not be
// logged.
func (c Config) DSN() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.Username, c.Password),
		Host:     net.JoinHostPort(c.Host, c.Port),
		Path:     c.Database,
		RawQuery: url.Values{"sslmode": {c.SSLMode}}.Encode(),
	}
	return u.String()
}

// DB is a PostgreSQL database.
type DB struct {
	sql *sql.DB
}

func open(get config.Getter, pool database.PoolConfig) (database.Database, error) {
	cfg, err := LoadConfig(get)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		// The cause can quote the DSN, so it is deliberately left out.
		return nil, errors.New("invalid connection settings")
	}
	db.SetMaxOpenConns(pool.MaxOpenConns)
	db.SetMaxIdleConns(pool.MaxIdleConns)
	db.SetConnMaxLifetime(pool.ConnMaxLifetime)
	db.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
	return &DB{sql: db}, nil
}

// Ping verifies the database is reachable.
func (d *DB) Ping(ctx context.Context) error {
	return d.sql.PingContext(ctx)
}

// Close releases all connections.
func (d *DB) Close() error {
	return d.sql.Close()
}

// Migrate applies pending migrations in file name order, each in its own
// transaction. An advisory lock keeps concurrent instances from racing.
func (d *DB) Migrate(ctx context.Context) error {
	return d.apply(ctx, migrations.Postgres, "postgres")
}

func (d *DB) apply(ctx context.Context, files fs.FS, dir string) (err error) {
	// Advisory locks belong to a session, so everything uses one connection.
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(migrationLockID)); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// ctx may already be cancelled, and the lock must still be released.
		_, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(migrationLockID))
		if unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", unlockErr))
		}
	}()

	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	if _, err := conn.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries { // fs.ReadDir returns entries sorted by name
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		body, err := fs.ReadFile(files, path.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if err := applyOne(ctx, conn, entry.Name(), string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, version, body string) error {
	var applied bool
	err := conn.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
	).Scan(&applied)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful commit

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return err
	}
	return tx.Commit()
}
