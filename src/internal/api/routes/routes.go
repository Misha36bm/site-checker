// Package routes maps URLs to handlers.
package routes

import (
	"net/http"

	"github.com/Misha36bm/site-checker/src/internal/api/handlers"
	"github.com/Misha36bm/site-checker/src/internal/api/middleware"
)

// Deps holds what the handlers need.
type Deps struct {
	DB           handlers.Pinger
	Checks       handlers.CheckService
	MaxBodyBytes int64
}

// New returns the API handler.
func New(deps Deps) http.Handler {
	checks := handlers.Checks{Service: deps.Checks, MaxBodyBytes: deps.MaxBodyBytes}

	mux := http.NewServeMux()
	mux.Handle("GET /health", handlers.Health(deps.DB))
	mux.HandleFunc("POST /api/v1/checks", checks.Create)
	mux.HandleFunc("GET /api/v1/checks/{id}", checks.Get)

	return middleware.Logging(middleware.Recover(jsonRoutingErrors(mux)))
}

// jsonRoutingErrors gives the mux's own 404 and 405 responses the API's error
// format. The mux reports an empty pattern when no route will handle the request.
func jsonRoutingErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(&routingErrorWriter{ResponseWriter: w}, r)
	})
}

// routingErrorWriter replaces the mux's plain-text error body.
type routingErrorWriter struct {
	http.ResponseWriter
}

func (w *routingErrorWriter) WriteHeader(code int) {
	// The mux sets these for its plain-text body; Allow is kept.
	w.Header().Del("Content-Type")
	w.Header().Del("X-Content-Type-Options")
	switch code {
	case http.StatusMethodNotAllowed:
		handlers.WriteError(w.ResponseWriter, code, handlers.CodeMethodNotAllowed, "Method not allowed.")
	case http.StatusNotFound:
		handlers.WriteError(w.ResponseWriter, code, handlers.CodeNotFound, "Not found.")
	default:
		// Redirects from path cleaning and similar responses pass through.
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *routingErrorWriter) Write(b []byte) (int, error) {
	return len(b), nil // the original body is replaced in WriteHeader
}
