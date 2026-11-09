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
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

const instrumentationName = "github.com/zahra-ove/ticketing/services/event/internal/app"

// InstrumentedService decorates Service with a span per use case and business
// metrics. Keeping telemetry in a decorator leaves the use cases readable.
type InstrumentedService struct {
	next        *Service
	tracer      trace.Tracer
	created     metric.Int64Counter
	transitions metric.Int64Counter
}

// NewInstrumentedService wraps next using the global tracer and meter providers.
func NewInstrumentedService(next *Service) (*InstrumentedService, error) {
	meter := otel.Meter(instrumentationName)

	created, err := meter.Int64Counter("ticketing.events.created",
		metric.WithDescription("Number of events created."),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}

	transitions, err := meter.Int64Counter("ticketing.events.transitions",
		metric.WithDescription("Number of event status changes, by target status."),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}

	return &InstrumentedService{
		next:        next,
		tracer:      otel.Tracer(instrumentationName),
		created:     created,
		transitions: transitions,
	}, nil
}

// isExpected reports domain errors caused by the client, not by the service.
func isExpected(err error) bool {
	var verr *domain.ValidationError
	return errors.As(err, &verr) ||
		errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrInvalidTransition)
}

var eventIDKey = attribute.Key("ticketing.event.id")

// CreateEvent implements the use case with tracing and metrics.
func (s *InstrumentedService) CreateEvent(ctx context.Context, p domain.NewEventParams) (ev *domain.Event, err error) {
	ctx, span := s.tracer.Start(ctx, "EventService.CreateEvent",
		trace.WithAttributes(attribute.Int("ticketing.event.sections", len(p.Sections))))
	defer func() { telemetry.End(span, err, isExpected) }()

	ev, err = s.next.CreateEvent(ctx, p)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(eventIDKey.String(ev.ID.String()), attribute.Int("ticketing.event.capacity", ev.Capacity()))
	s.created.Add(ctx, 1)
	return ev, nil
}

// GetEvent implements the use case with tracing.
func (s *InstrumentedService) GetEvent(ctx context.Context, id uuid.UUID) (ev *domain.Event, err error) {
	ctx, span := s.tracer.Start(ctx, "EventService.GetEvent", trace.WithAttributes(eventIDKey.String(id.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	return s.next.GetEvent(ctx, id)
}

// ListPublishedEvents implements the use case with tracing.
func (s *InstrumentedService) ListPublishedEvents(ctx context.Context, cursor string, limit int) (page Page, err error) {
	ctx, span := s.tracer.Start(ctx, "EventService.ListPublishedEvents",
		trace.WithAttributes(attribute.Int("ticketing.page.limit", limit), attribute.Bool("ticketing.page.has_cursor", cursor != "")))
	defer func() { telemetry.End(span, err, isExpected) }()

	page, err = s.next.ListPublishedEvents(ctx, cursor, limit)
	span.SetAttributes(attribute.Int("ticketing.page.size", len(page.Events)))
	return page, err
}

// PublishEvent implements the use case with tracing and metrics.
func (s *InstrumentedService) PublishEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	return s.transition(ctx, "EventService.PublishEvent", id, s.next.PublishEvent)
}

// CancelEvent implements the use case with tracing and metrics.
func (s *InstrumentedService) CancelEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	return s.transition(ctx, "EventService.CancelEvent", id, s.next.CancelEvent)
}

func (s *InstrumentedService) transition(
	ctx context.Context, name string, id uuid.UUID,
	fn func(context.Context, uuid.UUID) (*domain.Event, error),
) (ev *domain.Event, err error) {
	ctx, span := s.tracer.Start(ctx, name, trace.WithAttributes(eventIDKey.String(id.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	ev, err = fn(ctx, id)
	if err != nil {
		return nil, err
	}
	s.transitions.Add(ctx, 1, metric.WithAttributes(attribute.String("status", string(ev.Status))))
	return ev, nil
}

// GetSeats implements the use case with tracing.
func (s *InstrumentedService) GetSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (res SeatLookup, err error) {
	ctx, span := s.tracer.Start(ctx, "EventService.GetSeats", trace.WithAttributes(
		eventIDKey.String(eventID.String()),
		attribute.Int("ticketing.seats.requested", len(seatIDs)),
	))
	defer func() { telemetry.End(span, err, isExpected) }()

	res, err = s.next.GetSeats(ctx, eventID, seatIDs)
	span.SetAttributes(attribute.Int("ticketing.seats.missing", len(res.Missing)))
	return res, err
}

// ListSeats implements the use case with tracing.
func (s *InstrumentedService) ListSeats(ctx context.Context, eventID uuid.UUID) (seats []domain.Seat, err error) {
	ctx, span := s.tracer.Start(ctx, "EventService.ListSeats", trace.WithAttributes(eventIDKey.String(eventID.String())))
	defer func() { telemetry.End(span, err, isExpected) }()

	seats, err = s.next.ListSeats(ctx, eventID)
	span.SetAttributes(attribute.Int("ticketing.seats.count", len(seats)))
	return seats, err
}
