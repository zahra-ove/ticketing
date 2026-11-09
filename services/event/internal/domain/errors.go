package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors returned by the domain and by adapters. Callers compare them
// with errors.Is; the transport layer maps them to protocol status codes.
var (
	// ErrNotFound means the requested event does not exist.
	ErrNotFound = errors.New("event not found")
	// ErrInvalidTransition means a status change is not allowed from the current status.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// ValidationError lists every invalid input field with a human-readable reason.
type ValidationError struct {
	Fields map[string]string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, e.Fields[k]))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// validator collects field errors so that a caller sees all problems at once.
type validator struct {
	fields map[string]string
}

func (v *validator) check(ok bool, field, msg string) {
	if ok {
		return
	}
	if v.fields == nil {
		v.fields = make(map[string]string)
	}
	if _, exists := v.fields[field]; !exists {
		v.fields[field] = msg
	}
}

func (v *validator) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields}
}
