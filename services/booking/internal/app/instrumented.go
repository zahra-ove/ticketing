package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/zahra-ove/ticketing/pkg/telemetry"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

const instrumentationName = "github.com/zahra-ove/ticketing/services/booking/internal/app"

var (
	eventIDKey   = attribute.Key("ticketing.event.id")
	holdIDKey    = attribute.Key("ticketing.hold.id")
	bookingIDKey = attribute.Key("ticketing.booking.id")
	resultKey    = attribute.Key("result")
)

// InstrumentedService decorates Service with a span per use case and business metrics.
type InstrumentedService struct {
	next      *Service
	tracer    trace.Tracer
	holds     metric.Int64Counter
	bookings  metric.Int64Counter
	cancelled metric.Int64Counter
}

// NewInstrumentedService wraps next using the global tracer and meter providers.
func NewInstrumentedService(next *Service) (*InstrumentedService, error) {
	meter := otel.Meter(instrumentationName)
	s := &InstrumentedService{next: next, tracer: otel.Tracer(instrumentationName)}

	var err error
	if s.holds, err = meter.Int64Counter("ticketing.holds.attempts",
		metric.WithDescription("Seat hold attempts by result: ok, unavailable (contention), rejected, error."),
		metric.WithUnit("{hold}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	if s.bookings, err = meter.Int64Counter("ticketing.bookings.created",
		metric.WithDescription("Bookings created from holds."), metric.WithUnit("{booking}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	if s.cancelled, err = meter.Int64Counter("ticketing.bookings.cancelled",
		metric.WithDescription("Bookings cancelled before payment."), metric.WithUnit("{booking}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	return s, nil
}

// isExpected reports errors caused by the client or by normal contention.
func isExpected(err error) bool {
	var (
		verr *domain.ValidationError
		uerr *domain.SeatsUnavailableError
	)
	return errors.As(err, &verr) || errors.As(err, &uerr) ||
		errors.Is(err, domain.ErrHoldNotFound) ||
		errors.Is(err, domain.ErrBookingNotFound) ||
		errors.Is(err, domain.ErrEventNotFound) ||
		errors.Is(err, domain.ErrEventNotBookable) ||
		errors.Is(err, domain.ErrInvalidTransition)
}

func holdResult(err error) string {
	var uerr *domain.SeatsUnavailableError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &uerr):
		return "unavailable"
	case isExpected(err):
		return "rejected"
	default:
		return "error"
	}
}

// HoldSeats implements the use case with tracing and metrics.
func (s *InstrumentedService) HoldSeats(ctx context.Context, customerID, eventID uuid.UUID, seatIDs []uuid.UUID) (h *domain.Hold, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.HoldSeats", trace.WithAttributes(
		eventIDKey.String(eventID.String()), attribute.Int("ticketing.seats.requested", len(seatIDs))))
	defer func() {
		s.holds.Add(ctx, 1, metric.WithAttributes(resultKey.String(holdResult(err))))
		telemetry.End(span, err, isExpected)
	}()

	h, err = s.next.HoldSeats(ctx, customerID, eventID, seatIDs)
	if err == nil {
		span.SetAttributes(holdIDKey.String(h.ID.String()))
	}
	return h, err
}

// GetHold implements the use case with tracing.
func (s *InstrumentedService) GetHold(ctx context.Context, customerID, holdID uuid.UUID) (h *domain.Hold, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.GetHold", trace.WithAttributes(holdIDKey.String(holdID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()
	return s.next.GetHold(ctx, customerID, holdID)
}

// ReleaseHold implements the use case with tracing.
func (s *InstrumentedService) ReleaseHold(ctx context.Context, customerID, holdID uuid.UUID) (err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.ReleaseHold", trace.WithAttributes(holdIDKey.String(holdID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()
	return s.next.ReleaseHold(ctx, customerID, holdID)
}

// CreateBooking implements the use case with tracing and metrics.
func (s *InstrumentedService) CreateBooking(ctx context.Context, customerID, holdID uuid.UUID) (b *domain.Booking, created bool, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.CreateBooking", trace.WithAttributes(holdIDKey.String(holdID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	b, created, err = s.next.CreateBooking(ctx, customerID, holdID)
	if err != nil {
		return nil, false, err
	}
	span.SetAttributes(bookingIDKey.String(b.ID.String()), attribute.Bool("ticketing.booking.idempotent_replay", !created))
	if created {
		s.bookings.Add(ctx, 1)
	}
	return b, created, nil
}

// GetBooking implements the use case with tracing.
func (s *InstrumentedService) GetBooking(ctx context.Context, customerID, id uuid.UUID) (b *domain.Booking, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.GetBooking", trace.WithAttributes(bookingIDKey.String(id.String())))
	defer func() { telemetry.End(span, err, isExpected) }()
	return s.next.GetBooking(ctx, customerID, id)
}

// CancelBooking implements the use case with tracing and metrics.
func (s *InstrumentedService) CancelBooking(ctx context.Context, customerID, id uuid.UUID) (b *domain.Booking, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.CancelBooking", trace.WithAttributes(bookingIDKey.String(id.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	b, err = s.next.CancelBooking(ctx, customerID, id)
	if err == nil {
		s.cancelled.Add(ctx, 1)
	}
	return b, err
}

// GetAvailability implements the use case with tracing.
func (s *InstrumentedService) GetAvailability(ctx context.Context, eventID uuid.UUID) (av Availability, err error) {
	ctx, span := s.tracer.Start(ctx, "BookingService.GetAvailability", trace.WithAttributes(eventIDKey.String(eventID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	av, err = s.next.GetAvailability(ctx, eventID)
	span.SetAttributes(attribute.Int("ticketing.seats.booked", len(av.Booked)), attribute.Int("ticketing.seats.held", len(av.Held)))
	return av, err
}
