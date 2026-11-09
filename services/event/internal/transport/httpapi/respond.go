package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// problem is an RFC 9457 "Problem Details for HTTP APIs" response body.
type problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent; an encode error can only be logged by
	// the caller's middleware via the broken connection, so it is ignored here.
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, r *http.Request, p problem) {
	p.Type = "about:blank"
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	p.RequestID = RequestIDFrom(r.Context())

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// badRequest is returned for malformed requests (bad JSON, bad path or query values).
type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

// writeError maps an error to an HTTP problem response. Unknown errors become
// 500 and are logged with their full chain; their details never reach the client.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		verr *domain.ValidationError
		berr badRequest
	)
	switch {
	case errors.As(err, &berr):
		writeProblem(w, r, problem{Status: http.StatusBadRequest, Detail: berr.msg})
	case errors.As(err, &verr):
		writeProblem(w, r, problem{Status: http.StatusUnprocessableEntity, Detail: "request validation failed", Errors: verr.Fields})
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, problem{Status: http.StatusNotFound, Detail: domain.ErrNotFound.Error()})
	case errors.Is(err, domain.ErrInvalidTransition):
		writeProblem(w, r, problem{Status: http.StatusConflict, Detail: err.Error()})
	default:
		h.log.ErrorContext(r.Context(), "request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.Any("error", err),
		)
		writeProblem(w, r, problem{Status: http.StatusInternalServerError})
	}
}

// decodeJSON reads exactly one JSON object from the body into dst and rejects
// unknown fields, oversized bodies and trailing data.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var (
			syntaxErr *json.SyntaxError
			typeErr   *json.UnmarshalTypeError
			maxErr    *http.MaxBytesError
		)
		switch {
		case errors.Is(err, io.EOF):
			return badRequest{"request body must not be empty"}
		case errors.As(err, &syntaxErr):
			return badRequest{fmt.Sprintf("malformed JSON at offset %d", syntaxErr.Offset)}
		case errors.As(err, &typeErr):
			return badRequest{fmt.Sprintf("field %q has the wrong type", typeErr.Field)}
		case errors.As(err, &maxErr):
			return badRequest{fmt.Sprintf("request body must not exceed %d bytes", maxErr.Limit)}
		default:
			// Unknown fields and time parse errors land here; their messages are safe to show.
			return badRequest{err.Error()}
		}
	}
	if dec.More() {
		return badRequest{"request body must contain a single JSON object"}
	}
	return nil
}
