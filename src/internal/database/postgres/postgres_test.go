package postgres

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Misha36bm/site-checker/src/internal/database"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func validEnv() map[string]string {
	return map[string]string{
		"POSTGRES_HOST":     "postgres",
		"POSTGRES_DATABASE": "site_checker",
		"POSTGRES_USERNAME": "site_checker",
		"POSTGRES_PASSWORD": "p@ss/w:rd?",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "5432" || cfg.SSLMode != "disable" {
		t.Errorf("unexpected defaults: port %q, sslmode %q", cfg.Port, cfg.SSLMode)
	}
}

func TestLoadConfigNamesAllMissingSettings(t *testing.T) {
	_, err := LoadConfig(env(nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"POSTGRES_HOST", "POSTGRES_DATABASE", "POSTGRES_USERNAME", "POSTGRES_PASSWORD"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}

func TestLoadConfigRejectsUnknownSSLMode(t *testing.T) {
	values := validEnv()
	values["POSTGRES_SSLMODE"] = "maybe"
	_, err := LoadConfig(env(values))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), values["POSTGRES_PASSWORD"]) {
		t.Errorf("error exposes the password: %v", err)
	}
}

func TestDSNEscapesCredentials(t *testing.T) {
	cfg, err := LoadConfig(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != cfg.Password {
		t.Errorf("password = %q, want %q", password, cfg.Password)
	}
	if u.Host != "postgres:5432" || u.Path != "/site_checker" || u.Query().Get("sslmode") != "disable" {
		t.Errorf("unexpected DSN parts: %+v", u)
	}
}

func TestDriverIsRegistered(t *testing.T) {
	db, err := database.Open(database.Config{Driver: DriverName}, env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDriverReportsMissingSettings(t *testing.T) {
	if _, err := database.Open(database.Config{Driver: DriverName}, env(nil)); err == nil {
		t.Fatal("expected an error")
	}
}
