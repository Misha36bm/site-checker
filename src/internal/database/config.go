package database

import (
	"errors"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/config"
)

// PoolConfig holds connection pool limits. They apply to whichever driver is
// selected, unlike connection parameters, which each driver reads for itself.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Config selects a driver and sets the options common to all drivers.
type Config struct {
	Driver         string
	Pool           PoolConfig
	ConnectTimeout time.Duration
}

// LoadConfig reads the driver selection and the common options.
func LoadConfig(get config.Getter) (Config, error) {
	driver := get("DB_DRIVER")
	if driver == "" {
		return Config{}, errors.New("DB_DRIVER is required")
	}
	maxOpen, err := config.Int(get, "DB_MAX_OPEN_CONNS", 20, 1)
	if err != nil {
		return Config{}, err
	}
	maxIdle, err := config.Int(get, "DB_MAX_IDLE_CONNS", 5, 0)
	if err != nil {
		return Config{}, err
	}
	lifetime, err := config.Duration(get, "DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	idleTime, err := config.Duration(get, "DB_CONN_MAX_IDLE_TIME", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	connectTimeout, err := config.Duration(get, "DB_CONNECT_TIMEOUT", time.Minute)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Driver: driver,
		Pool: PoolConfig{
			MaxOpenConns:    maxOpen,
			MaxIdleConns:    maxIdle,
			ConnMaxLifetime: lifetime,
			ConnMaxIdleTime: idleTime,
		},
		ConnectTimeout: connectTimeout,
	}, nil
}
