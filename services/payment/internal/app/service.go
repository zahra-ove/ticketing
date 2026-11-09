// Package app holds the use cases of the payment service: charging for new
// bookings and refunding when the booking saga compensates.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

// DeclinedError is returned by a Provider when the charge is refused for a
// business reason (card declined, insufficient funds). It is final: retrying
// will not help, so it turns into a failed payment.
type DeclinedError struct{ Reason string }

func (e *DeclinedError) Error() string { return "payment declined: " + e.Reason }

// ChargeRequest is sent to the payment provider.
type ChargeRequest struct {
	// IdempotencyKey makes repeated charges with the same key charge only once.
	IdempotencyKey string
	CustomerID     uuid.UUID
	AmountCents    int64
}

// Provider is the external payment provider (a card processor).
// Any error other than *DeclinedError is treated as transient and retried.
type Provider interface {
	Charge(ctx context.Context, req ChargeRequest) (reference string, err error)
	Refund(ctx context.Context, reference, idempotencyKey string) error
}

// ErrDuplicatePayment is returned by Repository.Create when the booking
// already has a payment.
var ErrDuplicatePayment = errors.New("booking already has a payment")

// Repository persists payments and their events.
type Repository interface {
	// Create stores a payment and its events; ErrDuplicatePayment if the booking already has one.
	Create(ctx context.Context, p *domain.Payment) error
	// GetByBookingID returns the payment of a booking or domain.ErrPaymentNotFound.
	GetByBookingID(ctx context.Context, bookingID uuid.UUID) (*domain.Payment, error)
	// Update loads the payment, applies fn and saves it with its events, with the row locked.
	Update(ctx context.Context, id uuid.UUID, fn func(p *domain.Payment) error) (*domain.Payment, error)
}

// Service implements the payment use cases.
type Service struct {
	provider Provider
	repo     Repository
	now      func() time.Time
}

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the clock, which is useful in tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService returns a Service.
func NewService(provider Provider, repo Repository, opts ...Option) *Service {
	s := &Service{provider: provider, repo: repo, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ChargeBooking charges the customer for a new booking. It is idempotent per
// booking: a redelivered BookingCreated never charges twice, because
//   - an existing payment for the booking short-circuits the call, and
//   - the provider receives the booking id as idempotency key, which covers a
//     crash between charging and storing the payment.
//
// charged is false when the booking had already been handled (a redelivery).
func (s *Service) ChargeBooking(ctx context.Context, c domain.Charge) (p *domain.Payment, charged bool, err error) {
	switch existing, err := s.repo.GetByBookingID(ctx, c.BookingID); {
	case err == nil:
		return existing, false, nil // already handled
	case !errors.Is(err, domain.ErrPaymentNotFound):
		return nil, false, fmt.Errorf("look up payment: %w", err)
	}

	ref, err := s.provider.Charge(ctx, ChargeRequest{
		IdempotencyKey: "charge-" + c.BookingID.String(),
		CustomerID:     c.CustomerID,
		AmountCents:    c.AmountCents,
	})

	var declined *DeclinedError
	switch {
	case errors.As(err, &declined):
		p, err = domain.NewFailed(c, declined.Reason, s.now())
	case err != nil:
		return nil, false, fmt.Errorf("charge booking %s: %w", c.BookingID, err) // transient: the consumer retries
	default:
		p, err = domain.NewSucceeded(c, ref, s.now())
	}
	if err != nil {
		return nil, false, err
	}

	switch err := s.repo.Create(ctx, p); {
	case errors.Is(err, ErrDuplicatePayment):
		// A concurrent delivery stored it first; the provider charged only once thanks to the idempotency key.
		existing, err := s.repo.GetByBookingID(ctx, c.BookingID)
		return existing, false, err
	case err != nil:
		return nil, false, fmt.Errorf("store payment: %w", err)
	}
	return p, true, nil
}

// RefundBooking gives the money back for a booking that was cancelled after
// it was paid. It is idempotent: a refunded payment is left alone, and the
// provider refund uses an idempotency key.
func (s *Service) RefundBooking(ctx context.Context, bookingID uuid.UUID) error {
	p, err := s.repo.GetByBookingID(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("get payment of booking %s: %w", bookingID, err)
	}
	if p.Status != domain.StatusSucceeded {
		return nil // already refunded, or failed: nothing to give back
	}

	if err := s.provider.Refund(ctx, p.ProviderRef, "refund-"+p.ID.String()); err != nil {
		return fmt.Errorf("refund payment %s: %w", p.ID, err)
	}

	_, err = s.repo.Update(ctx, p.ID, func(p *domain.Payment) error {
		if p.Status == domain.StatusRefunded {
			return errNoChange
		}
		return p.Refund(s.now())
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return fmt.Errorf("mark payment %s refunded: %w", p.ID, err)
	}
	return nil
}

var errNoChange = errors.New("no change")

// GetByBookingID returns the payment of a booking.
func (s *Service) GetByBookingID(ctx context.Context, bookingID uuid.UUID) (*domain.Payment, error) {
	p, err := s.repo.GetByBookingID(ctx, bookingID)
	if err != nil {
		return nil, fmt.Errorf("get payment of booking %s: %w", bookingID, err)
	}
	return p, nil
}
