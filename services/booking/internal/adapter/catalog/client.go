// Package catalog reads events and seats from the event service over gRPC.
package catalog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	eventv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/event/v1"
	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// Client implements app.Catalog.
type Client struct {
	api eventv1.CatalogServiceClient
}

var _ app.Catalog = (*Client)(nil)

// NewClient returns a Client that uses conn.
func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{api: eventv1.NewCatalogServiceClient(conn)}
}

// GetSeats implements app.Catalog.
func (c *Client) GetSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (app.CatalogResult, error) {
	req := &eventv1.GetSeatsRequest{EventId: eventID.String(), SeatIds: make([]string, len(seatIDs))}
	for i, id := range seatIDs {
		req.SeatIds[i] = id.String()
	}

	resp, err := c.api.GetSeats(ctx, req)
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			return app.CatalogResult{}, domain.ErrEventNotFound
		case codes.InvalidArgument:
			return app.CatalogResult{}, domain.NewValidationError("seat_ids", status.Convert(err).Message())
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
			return app.CatalogResult{}, fmt.Errorf("event catalog: %w: %w", app.ErrDependencyUnavailable, err)
		default:
			return app.CatalogResult{}, fmt.Errorf("event catalog: %w", err)
		}
	}

	res := app.CatalogResult{
		Event: app.CatalogEvent{
			Published: resp.GetEvent().GetStatus() == eventv1.EventStatus_EVENT_STATUS_PUBLISHED,
			StartsAt:  resp.GetEvent().GetStartsAt().AsTime(),
		},
		Seats:   make([]domain.Seat, 0, len(resp.GetSeats())),
		Missing: make([]uuid.UUID, 0, len(resp.GetMissingSeatIds())),
	}
	for _, s := range resp.GetSeats() {
		id, err := uuid.Parse(s.GetId())
		if err != nil {
			return app.CatalogResult{}, fmt.Errorf("event catalog returned invalid seat id %q: %w", s.GetId(), err)
		}
		res.Seats = append(res.Seats, domain.Seat{ID: id, Label: s.GetLabel(), PriceCents: s.GetPriceCents()})
	}
	for _, raw := range resp.GetMissingSeatIds() {
		if id, err := uuid.Parse(raw); err == nil {
			res.Missing = append(res.Missing, id)
		}
	}
	return res, nil
}
