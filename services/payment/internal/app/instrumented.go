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
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

const instrumentationName = "github.com/zahra-ove/ticketing/services/payment/internal/app"

var bookingIDKey = attribute.Key("ticketing.booking.id")

// InstrumentedService decorates Service with spans and payment metrics.
type InstrumentedService struct {
	next     *Service
	tracer   trace.Tracer
	payments metric.Int64Counter
	refunds  metric.Int64Counter
}

// NewInstrumentedService wraps next using the global providers.
func NewInstrumentedService(next *Service) (*InstrumentedService, error) {
	meter := otel.Meter(instrumentationName)
	s := &InstrumentedService{next: next, tracer: otel.Tracer(instrumentationName)}
	var err error
	if s.payments, err = meter.Int64Counter("ticketing.payments.processed",
		metric.WithDescription("Bookings charged, by outcome."), metric.WithUnit("{payment}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	if s.refunds, err = meter.Int64Counter("ticketing.payments.refunds",
		metric.WithDescription("Refund requests handled (saga compensations)."), metric.WithUnit("{refund}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	return s, nil
}

func isExpected(err error) bool { return errors.Is(err, domain.ErrPaymentNotFound) }

// ChargeBooking implements the use case with tracing and metrics.
func (s *InstrumentedService) ChargeBooking(ctx context.Context, c domain.Charge) (p *domain.Payment, charged bool, err error) {
	ctx, span := s.tracer.Start(ctx, "PaymentService.ChargeBooking", trace.WithAttributes(
		bookingIDKey.String(c.BookingID.String()), attribute.Int64("ticketing.amount_cents", c.AmountCents)))
	defer func() { telemetry.End(span, err, isExpected) }()

	p, charged, err = s.next.ChargeBooking(ctx, c)
	outcome := "error" // transient failure, will be retried
	switch {
	case err == nil && !charged:
		outcome = "duplicate"
	case err == nil:
		outcome = string(p.Status) // succeeded or failed (declined)
	}
	span.SetAttributes(attribute.String("ticketing.payment.outcome", outcome))
	s.payments.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	return p, charged, err
}

// RefundBooking implements the use case with tracing and metrics.
func (s *InstrumentedService) RefundBooking(ctx context.Context, bookingID uuid.UUID) (err error) {
	ctx, span := s.tracer.Start(ctx, "PaymentService.RefundBooking", trace.WithAttributes(bookingIDKey.String(bookingID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	if err = s.next.RefundBooking(ctx, bookingID); err == nil {
		s.refunds.Add(ctx, 1)
	}
	return err
}

// GetByBookingID implements the use case with tracing.
func (s *InstrumentedService) GetByBookingID(ctx context.Context, bookingID uuid.UUID) (p *domain.Payment, err error) {
	ctx, span := s.tracer.Start(ctx, "PaymentService.GetByBookingID", trace.WithAttributes(bookingIDKey.String(bookingID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()
	return s.next.GetByBookingID(ctx, bookingID)
}
