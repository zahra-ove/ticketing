// Package domain contains the payment model. A payment is made once per
// booking; it either succeeds or fails, and a succeeded payment can be refunded.
package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Errors returned by the domain and the adapters.
var (
	// ErrPaymentNotFound means no payment exists for the booking.
	ErrPaymentNotFound = errors.New("payment not found")
	// ErrInvalidTransition means a status change is not allowed.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// Status of a payment.
type Status string

// Payment statuses.
const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusRefunded  Status = "refunded"
)

// Payment is the result of charging the customer for a booking.
type Payment struct {
	ID            uuid.UUID
	BookingID     uuid.UUID
	CustomerID    uuid.UUID
	AmountCents   int64
	Status        Status
	FailureReason string
	// ProviderRef is the charge reference at the payment provider.
	ProviderRef string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	events []Event
}

// Charge describes what is being paid for.
type Charge struct {
	BookingID   uuid.UUID
	CustomerID  uuid.UUID
	AmountCents int64
}

func newPayment(c Charge, now time.Time) (*Payment, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate payment id: %w", err)
	}
	now = now.UTC()
	return &Payment{
		ID:          id,
		BookingID:   c.BookingID,
		CustomerID:  c.CustomerID,
		AmountCents: c.AmountCents,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// NewSucceeded records a successful charge.
func NewSucceeded(c Charge, providerRef string, now time.Time) (*Payment, error) {
	p, err := newPayment(c, now)
	if err != nil {
		return nil, err
	}
	p.Status = StatusSucceeded
	p.ProviderRef = providerRef
	p.events = append(p.events, PaymentSucceeded{Payment: p.snapshot()})
	return p, nil
}

// NewFailed records a charge the provider declined.
func NewFailed(c Charge, reason string, now time.Time) (*Payment, error) {
	p, err := newPayment(c, now)
	if err != nil {
		return nil, err
	}
	p.Status = StatusFailed
	p.FailureReason = reason
	p.events = append(p.events, PaymentFailed{Payment: p.snapshot()})
	return p, nil
}

// Refund marks a succeeded payment as refunded.
func (p *Payment) Refund(now time.Time) error {
	if p.Status != StatusSucceeded {
		return fmt.Errorf("%w: cannot refund a %s payment", ErrInvalidTransition, p.Status)
	}
	p.Status = StatusRefunded
	p.UpdatedAt = now.UTC()
	p.events = append(p.events, PaymentRefunded{Payment: p.snapshot()})
	return nil
}

// PullEvents returns the recorded events and forgets them.
func (p *Payment) PullEvents() []Event {
	ev := p.events
	p.events = nil
	return ev
}

func (p *Payment) snapshot() Payment {
	cp := *p
	cp.events = nil
	return cp
}

// Event is something that happened to a payment.
type Event interface{ isPaymentEvent() }

// PaymentSucceeded is published so the booking gets confirmed.
type PaymentSucceeded struct{ Payment Payment }

// PaymentFailed is published so the booking gets cancelled.
type PaymentFailed struct{ Payment Payment }

// PaymentRefunded is published after a compensating refund.
type PaymentRefunded struct{ Payment Payment }

func (PaymentSucceeded) isPaymentEvent() {}
func (PaymentFailed) isPaymentEvent()    {}
func (PaymentRefunded) isPaymentEvent()  {}
