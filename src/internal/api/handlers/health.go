// Package handlers implements the HTTP handlers of the API.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const healthPingTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Browser  string `json:"browser"`
}

// Health reports application health and the state of its dependencies. An
// unreachable database means 503, because nothing works without it. An unreachable
// browser means status "degraded" with 200: checks still run and only screenshots
// fail, so nothing should treat the API as down. Causes are logged, not returned,
// because they can reveal internal addresses.
func Health(db, browser Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		// At the same time, so that two slow dependencies stay within the timeout of
		// whoever is asking.
		var dbErr, browserErr error
		var wg sync.WaitGroup
		wg.Go(func() { dbErr = db.Ping(ctx) })
		wg.Go(func() { browserErr = browser.Ping(ctx) })
		wg.Wait()

		body := healthResponse{Status: "ok", Database: "ok", Browser: "ok"}
		code := http.StatusOK
		if browserErr != nil {
			slog.Warn("health check: browser unreachable", "error", browserErr)
			body.Status, body.Browser = "degraded", "unreachable"
		}
		if dbErr != nil {
			slog.Error("health check: database unreachable", "error", dbErr)
			body.Status, body.Database = "unavailable", "unreachable"
			code = http.StatusServiceUnavailable
		}
		WriteJSON(w, code, body)
	}
}
