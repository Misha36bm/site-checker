// Package routes maps URLs to handlers.
package routes

import (
	"net/http"

	"github.com/Misha36bm/site-checker/src/internal/api/handlers"
)

// New returns the API handler.
func New(db handlers.Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handlers.Health(db))
	return mux
}
