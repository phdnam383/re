package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"re/internal/profilemanagement"
	"re/internal/rulemanagement"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "internal error"
	switch {
	case errors.Is(err, rulemanagement.ErrInvalid) || errors.Is(err, profilemanagement.ErrInvalid):
		status, code, message = 400, "invalid_request", err.Error()
	case errors.Is(err, rulemanagement.ErrNotFound) || errors.Is(err, profilemanagement.ErrNotFound):
		status, code, message = 404, "not_found", err.Error()
	case errors.Is(err, rulemanagement.ErrConflict) || errors.Is(err, profilemanagement.ErrConflict):
		status, code, message = 409, "conflict", err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = 504, "timeout", "request timed out"
	}
	if status >= 500 {
		h.log.ErrorContext(r.Context(), "management request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
