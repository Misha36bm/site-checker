package database

import "context"

// Database is the contract every database driver implements. Repository
// contracts are added here as the application needs them.
type Database interface {
	// Ping verifies the database is reachable.
	Ping(ctx context.Context) error
	// Migrate applies pending schema migrations. It is safe to call repeatedly
	// and from several processes at once.
	Migrate(ctx context.Context) error
	// Close releases all connections.
	Close() error
}
