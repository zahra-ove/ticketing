package grpcapi_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	eventv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/event/v1"
	"github.com/zahra-ove/ticketing/pkg/grpcx"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
	"github.com/zahra-ove/ticketing/services/event/internal/transport/grpcapi"
)

type stubGetter func(eventID uuid.UUID, seatIDs []uuid.UUID) (app.SeatLookup, error)

func (f stubGetter) GetSeats(_ context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (app.SeatLookup, error) {
	return f(eventID, seatIDs)
}

// newClient runs the real server (with all interceptors) over an in-memory
// connection, so the test covers serialization and status codes end to end.
func newClient(t *testing.T, svc grpcapi.SeatGetter) eventv1.CatalogServiceClient {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv, _ := grpcx.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	eventv1.RegisterCatalogServiceServer(srv, grpcapi.NewServer(svc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return eventv1.NewCatalogServiceClient(conn)
}

func TestGetSeats(t *testing.T) {
	t.Parallel()

	eventID, seatID, missingID := uuid.New(), uuid.New(), uuid.New()
	startsAt := time.Date(2027, 1, 1, 18, 0, 0, 0, time.UTC)

	client := newClient(t, stubGetter(func(gotEvent uuid.UUID, gotSeats []uuid.UUID) (app.SeatLookup, error) {
		if gotEvent != eventID || len(gotSeats) != 2 {
			t.Errorf("unexpected call: %s %v", gotEvent, gotSeats)
		}
		return app.SeatLookup{
			Event:   &domain.Event{ID: eventID, Title: "Go Meetup", StartsAt: startsAt, Status: domain.StatusPublished},
			Seats:   []domain.Seat{{ID: seatID, Section: "A", Row: 1, Number: 2, PriceCents: 1500}},
			Missing: []uuid.UUID{missingID},
		}, nil
	}))

	resp, err := client.GetSeats(t.Context(), &eventv1.GetSeatsRequest{
		EventId: eventID.String(),
		SeatIds: []string{seatID.String(), missingID.String()},
	})
	if err != nil {
		t.Fatalf("GetSeats() error = %v", err)
	}

	if resp.GetEvent().GetStatus() != eventv1.EventStatus_EVENT_STATUS_PUBLISHED || !resp.GetEvent().GetStartsAt().AsTime().Equal(startsAt) {
		t.Errorf("event = %v", resp.GetEvent())
	}
	if len(resp.GetSeats()) != 1 || resp.GetSeats()[0].GetLabel() != "A-1-2" || resp.GetSeats()[0].GetPriceCents() != 1500 {
		t.Errorf("seats = %v", resp.GetSeats())
	}
	if len(resp.GetMissingSeatIds()) != 1 || resp.GetMissingSeatIds()[0] != missingID.String() {
		t.Errorf("missing = %v", resp.GetMissingSeatIds())
	}
}

func TestGetSeats_StatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		eventID  string
		seatIDs  []string
		svcErr   error
		svcPanic bool
		want     codes.Code
	}{
		{name: "malformed event id", eventID: "nope", seatIDs: []string{uuid.NewString()}, want: codes.InvalidArgument},
		{name: "malformed seat id", eventID: uuid.NewString(), seatIDs: []string{"nope"}, want: codes.InvalidArgument},
		{name: "validation error", eventID: uuid.NewString(), svcErr: &domain.ValidationError{Fields: map[string]string{"seat_ids": "x"}}, want: codes.InvalidArgument},
		{name: "event not found", eventID: uuid.NewString(), seatIDs: []string{uuid.NewString()}, svcErr: domain.ErrNotFound, want: codes.NotFound},
		{name: "unexpected error", eventID: uuid.NewString(), seatIDs: []string{uuid.NewString()}, svcErr: io.ErrUnexpectedEOF, want: codes.Internal},
		{name: "panic is recovered", eventID: uuid.NewString(), seatIDs: []string{uuid.NewString()}, svcPanic: true, want: codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newClient(t, stubGetter(func(uuid.UUID, []uuid.UUID) (app.SeatLookup, error) {
				if tt.svcPanic {
					panic("boom")
				}
				return app.SeatLookup{}, tt.svcErr
			}))

			_, err := client.GetSeats(t.Context(), &eventv1.GetSeatsRequest{EventId: tt.eventID, SeatIds: tt.seatIDs})

			if got := status.Code(err); got != tt.want {
				t.Errorf("code = %s, want %s (err: %v)", got, tt.want, err)
			}
		})
	}
}
