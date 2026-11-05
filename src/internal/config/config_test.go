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

func TestLoadHTTPSettings(t *testing.T) {
	app, err := Load(env(map[string]string{
		"HTTP_MAX_REDIRECTS": "0",
		"SSRF_ALLOWED_CIDRS": "10.1.0.0/16, 192.168.1.5/24",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if app.HTTP.MaxRedirects != 0 || app.HTTP.Timeout != 15*time.Second {
		t.Errorf("HTTP = %+v", app.HTTP)
	}
	if len(app.AllowedCIDRs) != 2 || app.AllowedCIDRs[1].String() != "192.168.1.0/24" {
		t.Errorf("AllowedCIDRs = %v", app.AllowedCIDRs)
	}
}

func TestLoadBlocksEverythingByDefault(t *testing.T) {
	app, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(app.AllowedCIDRs) != 0 {
		t.Errorf("AllowedCIDRs = %v", app.AllowedCIDRs)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"port not a number":  {"APP_PORT": "http"},
		"port zero":          {"APP_PORT": "0"},
		"port too large":     {"APP_PORT": "70000"},
		"bad duration":       {"APP_SHUTDOWN_TIMEOUT": "soon"},
		"negative duration":  {"APP_SHUTDOWN_TIMEOUT": "-1s"},
		"no workers":         {"MAX_CONCURRENT_CHECKS": "0"},
		"zero poll":          {"WORKER_POLL_INTERVAL": "0s"},
		"short lock":         {"CHECK_LOCK_TTL": "1s"},
		"zero attempts":      {"CHECK_MAX_ATTEMPTS": "0"},
		"zero queue":         {"MAX_PENDING_CHECKS": "0"},
		"zero body limit":    {"API_MAX_BODY_BYTES": "0"},
		"zero http timeout":  {"HTTP_TIMEOUT": "0s"},
		"negative redirects": {"HTTP_MAX_REDIRECTS": "-1"},
		"bad cidr":           {"SSRF_ALLOWED_CIDRS": "10.0.0.0/8,nonsense"},
		"ip without mask":    {"SSRF_ALLOWED_CIDRS": "10.0.0.1"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
