package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Error codes returned by the API.
const (
	CodeInvalidRequest   = "invalid_request"
	CodeInvalidURL       = "invalid_url"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodePayloadTooLarge  = "payload_too_large"
	CodeQueueFull        = "queue_full"
	CodeInternal         = "internal_error"
)

// WriteJSON writes body with the given status code.
func WriteJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response", "error", err)
	}
}

// WriteError writes the API's error format. message must be safe to show clients.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	type detail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	WriteJSON(w, status, struct {
		Error detail `json:"error"`
	}{detail{code, message}})
}
