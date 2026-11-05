package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	cases := []struct {
		name     string
		pingErr  error
		code     int
		status   string
		database string
	}{
		{"database reachable", nil, http.StatusOK, "ok", "ok"},
		{"database down", errors.New("dial tcp 10.1.2.3:5432: refused"), http.StatusServiceUnavailable, "unavailable", "unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			New(fakePinger{tc.pingErr}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tc.code {
				t.Errorf("code = %d, want %d", rec.Code, tc.code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if strings.Contains(rec.Body.String(), "10.1.2.3") {
				t.Errorf("response exposes an internal address: %s", rec.Body)
			}
			var body struct{ Status, Database string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != tc.status || body.Database != tc.database {
				t.Errorf("body = %+v", body)
			}
		})
	}
}

func TestHealthRejectsOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	New(fakePinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d", rec.Code)
	}
}
