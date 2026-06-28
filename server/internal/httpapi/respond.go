package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cadence/server/internal/domain"
)

type errBody struct {
	Error errPayload `json:"error"`
}
type errPayload struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeError maps domain errors to HTTP status codes and a consistent body.
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, errBody{errPayload{Message: ve.Message, Field: ve.Field, Code: "invalid"}})
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody{errPayload{Message: "not found", Code: "not_found"}})
	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, errBody{errPayload{Message: "the resource was modified by someone else", Code: "conflict"}})
	case errors.Is(err, domain.ErrForbidden):
		writeJSON(w, http.StatusForbidden, errBody{errPayload{Message: "forbidden", Code: "forbidden"}})
	default:
		if log != nil {
			log.Error("internal error", "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errBody{errPayload{Message: "something went wrong", Code: "internal"}})
	}
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Invalid("body", "invalid JSON: "+err.Error())
	}
	return nil
}

// decodePatch reads a partial JSON object (used by PATCH). Unknown keys are
// allowed and ignored downstream so clients can evolve independently.
func decodePatch(r *http.Request) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(&m); err != nil {
		return nil, domain.Invalid("body", "invalid JSON: "+err.Error())
	}
	return m, nil
}
