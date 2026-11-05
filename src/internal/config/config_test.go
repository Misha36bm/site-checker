package config

import (
	"testing"
	"time"
)

func env(values map[string]string) Getter {
	return func(key string) string { return values[key] }
}

func TestLoadDefaults(t *testing.T) {
	app, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := app.Addr(), "0.0.0.0:8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
	if app.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s", app.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	app, err := Load(env(map[string]string{
		"APP_ENV":              "production",
		"APP_HOST":             "::",
		"APP_PORT":             "9000",
		"APP_SHUTDOWN_TIMEOUT": "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if app.Env != "production" || app.ShutdownTimeout != 3*time.Second {
		t.Errorf("unexpected config: %+v", app)
	}
	if got, want := app.Addr(), "[::]:9000"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"port not a number": {"APP_PORT": "http"},
		"port zero":         {"APP_PORT": "0"},
		"port too large":    {"APP_PORT": "70000"},
		"bad duration":      {"APP_SHUTDOWN_TIMEOUT": "soon"},
		"negative duration": {"APP_SHUTDOWN_TIMEOUT": "-1s"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
