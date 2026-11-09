package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Sentinel errors. The transport layer maps them to status codes.
var (
	// ErrHoldNotFound means the hold does not exist, has expired, or belongs to another customer.
	ErrHoldNotFound = errors.New("hold not found or expired")
	// ErrBookingNotFound means the booking does not exist or belongs to another customer.
	ErrBookingNotFound = errors.New("booking not found")
	// ErrEventNotFound means the event does not exist in the catalog.
	ErrEventNotFound = errors.New("event not found")
	// ErrEventNotBookable means the event is not published or has already started.
	ErrEventNotBookable = errors.New("event is not open for booking")
	// ErrInvalidTransition means a status change is not allowed from the current status.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// SeatsUnavailableError lists seats that are held by someone else or already booked.
type SeatsUnavailableError struct {
	SeatIDs []uuid.UUID
}

// Error implements the error interface.
func (e *SeatsUnavailableError) Error() string {
	ids := make([]string, len(e.SeatIDs))
	for i, id := range e.SeatIDs {
		ids[i] = id.String()
	}
	return "seats are not available: " + strings.Join(ids, ", ")
}

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
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s: %s", k, e.Fields[k])
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// NewValidationError returns a ValidationError for a single field.
func NewValidationError(field, msg string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: msg}}
}
