// Package httpx holds the HTTP building blocks shared by every service:
// JSON and RFC 9457 problem responses, request decoding, middleware and a
// server runner with graceful shutdown.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxBodyBytes limits the size of JSON request bodies.
const MaxBodyBytes = 1 << 20 // 1 MiB

// Problem is an RFC 9457 "Problem Details for HTTP APIs" response body.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	// Extra holds problem-specific members, e.g. the seats that are taken.
	Extra map[string]any `json:"extra,omitempty"`
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent; nothing useful can be done with an encode error.
	_ = json.NewEncoder(w).Encode(v)
}

// WriteProblem writes p as an application/problem+json response.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	p.RequestID = RequestIDFrom(r.Context())

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// BadRequestError marks a malformed request (bad JSON, bad path or query values).
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// BadRequest returns a *BadRequestError with msg.
func BadRequest(msg string) error { return &BadRequestError{Msg: msg} }

// DecodeJSON reads exactly one JSON object from the body into dst and rejects
// unknown fields, oversized bodies and trailing data with a *BadRequestError.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
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
			return BadRequest("request body must not be empty")
		case errors.As(err, &syntaxErr):
			return BadRequest(fmt.Sprintf("malformed JSON at offset %d", syntaxErr.Offset))
		case errors.As(err, &typeErr):
			return BadRequest(fmt.Sprintf("field %q has the wrong type", typeErr.Field))
		case errors.As(err, &maxErr):
			return BadRequest(fmt.Sprintf("request body must not exceed %d bytes", maxErr.Limit))
		default:
			// Unknown fields and time parse errors land here; their messages are safe to show.
			return BadRequest(err.Error())
		}
	}
	if dec.More() {
		return BadRequest("request body must contain a single JSON object")
	}
	return nil
}
