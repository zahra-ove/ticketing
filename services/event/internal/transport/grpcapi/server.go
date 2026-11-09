// Package grpcapi exposes the event catalog to other services over gRPC.
package grpcapi

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/event/v1"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// SeatGetter is the use case the gRPC API needs.
type SeatGetter interface {
	GetSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (app.SeatLookup, error)
}

// Server implements eventv1.CatalogServiceServer.
type Server struct {
	eventv1.UnimplementedCatalogServiceServer
	svc SeatGetter
}

// NewServer returns a Server backed by svc.
func NewServer(svc SeatGetter) *Server {
	return &Server{svc: svc}
}

// GetSeats implements eventv1.CatalogServiceServer.
func (s *Server) GetSeats(ctx context.Context, req *eventv1.GetSeatsRequest) (*eventv1.GetSeatsResponse, error) {
	eventID, err := uuid.Parse(req.GetEventId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "event_id must be a valid UUID")
	}
	seatIDs := make([]uuid.UUID, len(req.GetSeatIds()))
	for i, raw := range req.GetSeatIds() {
		if seatIDs[i], err = uuid.Parse(raw); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "seat_ids[%d] must be a valid UUID", i)
		}
	}

	res, err := s.svc.GetSeats(ctx, eventID, seatIDs)
	if err != nil {
		return nil, toStatus(err)
	}

	resp := &eventv1.GetSeatsResponse{
		Event: &eventv1.Event{
			Id:       res.Event.ID.String(),
			Title:    res.Event.Title,
			StartsAt: timestamppb.New(res.Event.StartsAt),
			Status:   toProtoStatus(res.Event.Status),
		},
		Seats:          make([]*eventv1.Seat, len(res.Seats)),
		MissingSeatIds: make([]string, len(res.Missing)),
	}
	for i, st := range res.Seats {
		resp.Seats[i] = &eventv1.Seat{Id: st.ID.String(), Label: st.Label(), PriceCents: st.PriceCents}
	}
	for i, id := range res.Missing {
		resp.MissingSeatIds[i] = id.String()
	}
	return resp, nil
}

// toStatus maps domain errors to gRPC status codes. Unknown errors become
// INTERNAL without details; the logging interceptor records the cause.
func toStatus(err error) error {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		return status.Error(codes.InvalidArgument, verr.Error())
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, "event not found")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}

func toProtoStatus(s domain.Status) eventv1.EventStatus {
	switch s {
	case domain.StatusDraft:
		return eventv1.EventStatus_EVENT_STATUS_DRAFT
	case domain.StatusPublished:
		return eventv1.EventStatus_EVENT_STATUS_PUBLISHED
	case domain.StatusCancelled:
		return eventv1.EventStatus_EVENT_STATUS_CANCELLED
	default:
		return eventv1.EventStatus_EVENT_STATUS_UNSPECIFIED
	}
}
