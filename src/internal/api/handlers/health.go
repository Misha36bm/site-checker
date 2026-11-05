// Package handlers implements the HTTP handlers of the API.
package handlers

import (
	"context"
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

		if err := db.Ping(ctx); err != nil {
			slog.Error("health check: database unreachable", "error", err)
			WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Database: "unreachable"})
			return
		}
		WriteJSON(w, http.StatusOK, healthResponse{Status: "ok", Database: "ok"})
	}
}
