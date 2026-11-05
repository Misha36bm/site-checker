// Package handlers implements the HTTP handlers of the API.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// Health reports application health and database connectivity. It responds 503
// if the database is unreachable. The cause is logged, not returned, because it
// can reveal internal addresses.
func Health(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		code, body := http.StatusOK, healthResponse{Status: "ok", Database: "ok"}
		if err := db.Ping(ctx); err != nil {
			slog.Error("health check: database unreachable", "error", err)
			code, body = http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Database: "unreachable"}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			slog.Error("health check: write response", "error", err)
		}
	}
}
