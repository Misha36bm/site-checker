// Package config reads application settings from the environment.
package config

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// Getter returns the value of a setting, or "" if it is unset. os.Getenv
// satisfies it; tests pass a map lookup instead.
type Getter func(key string) string

// App holds the settings of the application process itself. Database settings
// are loaded by the database package, because they depend on the driver.
type App struct {
	Env             string
	Host            string
	Port            int
	ShutdownTimeout time.Duration
}

// Addr returns the address the HTTP server listens on.
func (a App) Addr() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
}

// Load reads the application settings.
func Load(get Getter) (App, error) {
	port, err := Int(get, "APP_PORT", 8080, 1)
	if err != nil {
		return App{}, err
	}
	if port > 65535 {
		return App{}, fmt.Errorf("APP_PORT: %d is not a valid port", port)
	}
	shutdown, err := Duration(get, "APP_SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return App{}, err
	}
	return App{
		Env:             String(get, "APP_ENV", "development"),
		Host:            String(get, "APP_HOST", "0.0.0.0"),
		Port:            port,
		ShutdownTimeout: shutdown,
	}, nil
}

// String returns the setting, or def if it is unset.
func String(get Getter, key, def string) string {
	if v := get(key); v != "" {
		return v
	}
	return def
}

// Int returns the setting as an integer no smaller than minimum, or def if it
// is unset.
func Int(get Getter, key string, def, minimum int) (int, error) {
	v := get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	if n < minimum {
		return 0, fmt.Errorf("%s: %d is below the minimum of %d", key, n, minimum)
	}
	return n, nil
}

// Duration returns the setting as a non-negative duration, or def if it is
// unset.
func Duration(get Getter, key string, def time.Duration) (time.Duration, error) {
	v := get(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", key, v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: %s is negative", key, v)
	}
	return d, nil
}
