package httpadapter

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		domainErr = domain.NewInternalError("unexpected error")
	}

	writeJSON(w, domainErr.StatusCode, map[string]any{
		"trace_id": currentTraceID(r.Context()),
		"error": map[string]any{
			"code":       domainErr.Code,
			"message":    domainErr.Message,
			"error_type": domainErr.ErrorType,
		},
	})
}
