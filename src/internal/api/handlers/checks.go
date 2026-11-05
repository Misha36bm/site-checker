package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Misha36bm/site-checker/src/internal/checker"
	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

// retryAfterSeconds is sent with queue_full responses.
const retryAfterSeconds = "30"

// CheckService is the business logic the check handlers call.
type CheckService interface {
	Create(ctx context.Context, url string) (*models.Check, error)
	Get(ctx context.Context, id int64) (*models.Check, error)
}

// Checks implements the check endpoints.
type Checks struct {
	Service      CheckService
	MaxBodyBytes int64
}

// Create handles POST /api/v1/checks.
func (h Checks) Create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "Request body is too large.")
			return
		}
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, `Request body must be JSON like {"url": "https://example.com"}.`)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "Request body must contain one JSON object.")
		return
	}

	check, err := h.Service.Create(r.Context(), request.URL)
	switch {
	case err == nil:
		WriteJSON(w, http.StatusAccepted, struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			URL    string `json:"url"`
		}{check.ID, check.Status, check.URL})
	case netguard.IsInvalidURL(err):
		WriteError(w, http.StatusBadRequest, CodeInvalidURL, err.Error())
	case errors.Is(err, checker.ErrQueueFull):
		w.Header().Set("Retry-After", retryAfterSeconds)
		WriteError(w, http.StatusServiceUnavailable, CodeQueueFull, "Too many checks are waiting. Try again later.")
	default:
		internalError(w, r, err)
	}
}

// Get handles GET /api/v1/checks/{id}.
func (h Checks) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "Check ID must be a positive integer.")
		return
	}
	check, err := h.Service.Get(r.Context(), id)
	switch {
	case err == nil:
		WriteJSON(w, http.StatusOK, check)
	case errors.Is(err, checker.ErrCheckNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "Check not found.")
	default:
		internalError(w, r, err)
	}
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	WriteError(w, http.StatusInternalServerError, CodeInternal, "Internal server error.")
}
