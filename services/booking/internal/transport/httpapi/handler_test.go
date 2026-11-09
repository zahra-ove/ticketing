package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
	"github.com/zahra-ove/ticketing/services/booking/internal/transport/httpapi"
)

// stubService returns canned results; only the methods a test sets are used.
type stubService struct {
	hold    func(customerID, eventID uuid.UUID, seatIDs []uuid.UUID) (*domain.Hold, error)
	booking func(customerID, holdID uuid.UUID) (*domain.Booking, bool, error)
	cancel  func(customerID, id uuid.UUID) (*domain.Booking, error)
}

func (s stubService) HoldSeats(_ context.Context, c, e uuid.UUID, ids []uuid.UUID) (*domain.Hold, error) {
	return s.hold(c, e, ids)
}

func (s stubService) GetHold(context.Context, uuid.UUID, uuid.UUID) (*domain.Hold, error) {
	return nil, domain.ErrHoldNotFound
}
func (s stubService) ReleaseHold(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (s stubService) CreateBooking(_ context.Context, c, h uuid.UUID) (*domain.Booking, bool, error) {
	return s.booking(c, h)
}

func (s stubService) GetBooking(context.Context, uuid.UUID, uuid.UUID) (*domain.Booking, error) {
	return nil, domain.ErrBookingNotFound
}

func (s stubService) CancelBooking(_ context.Context, c, id uuid.UUID) (*domain.Booking, error) {
	return s.cancel(c, id)
}

func (s stubService) GetAvailability(context.Context, uuid.UUID) (app.Availability, error) {
	return app.Availability{}, nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newHandler(svc httpapi.BookingService) http.Handler {
	return httpapi.NewHandler(svc, map[string]httpx.Pinger{}, discard)
}

func do(t *testing.T, h http.Handler, method, path, body string, customer uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if customer != uuid.Nil {
		req.Header.Set(httpapi.CustomerIDHeader, customer.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHoldSeats(t *testing.T) {
	t.Parallel()

	customer, eventID, seatA, seatB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	body := fmt.Sprintf(`{"event_id":%q,"seat_ids":[%q,%q]}`, eventID, seatA, seatB)

	tests := []struct {
		name       string
		customer   uuid.UUID
		svcErr     error
		wantStatus int
	}{
		{name: "held", customer: customer, wantStatus: http.StatusCreated},
		{name: "no customer", customer: uuid.Nil, wantStatus: http.StatusUnauthorized},
		{name: "seats taken", customer: customer, svcErr: &domain.SeatsUnavailableError{SeatIDs: []uuid.UUID{seatB}}, wantStatus: http.StatusConflict},
		{name: "event not bookable", customer: customer, svcErr: domain.ErrEventNotBookable, wantStatus: http.StatusConflict},
		{name: "unknown event", customer: customer, svcErr: domain.ErrEventNotFound, wantStatus: http.StatusNotFound},
		{name: "invalid seats", customer: customer, svcErr: domain.NewValidationError("seat_ids", "x"), wantStatus: http.StatusUnprocessableEntity},
		{name: "catalog down", customer: customer, svcErr: fmt.Errorf("lookup: %w", app.ErrDependencyUnavailable), wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHandler(stubService{hold: func(c, e uuid.UUID, ids []uuid.UUID) (*domain.Hold, error) {
				if tt.svcErr != nil {
					return nil, tt.svcErr
				}
				if c != customer || e != eventID || len(ids) != 2 {
					t.Errorf("unexpected call: %s %s %v", c, e, ids)
				}
				return &domain.Hold{
					ID: uuid.New(), EventID: e, CustomerID: c, ExpiresAt: time.Now().Add(time.Minute),
					Seats: []domain.Seat{{ID: seatA, PriceCents: 100}, {ID: seatB, PriceCents: 200}},
				}, nil
			}})

			rec := do(t, h, http.MethodPost, "/v1/holds", body, tt.customer)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestHoldSeats_ConflictListsSeats(t *testing.T) {
	t.Parallel()
	taken := uuid.New()
	h := newHandler(stubService{hold: func(uuid.UUID, uuid.UUID, []uuid.UUID) (*domain.Hold, error) {
		return nil, &domain.SeatsUnavailableError{SeatIDs: []uuid.UUID{taken}}
	}})

	rec := do(t, h, http.MethodPost, "/v1/holds", fmt.Sprintf(`{"event_id":%q,"seat_ids":[%q]}`, uuid.New(), taken), uuid.New())

	var body struct {
		Extra struct {
			SeatIDs []uuid.UUID `json:"seat_ids"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Extra.SeatIDs) != 1 || body.Extra.SeatIDs[0] != taken {
		t.Errorf("seat_ids = %v, want [%s]", body.Extra.SeatIDs, taken)
	}
}

func TestCreateBooking_StatusReflectsIdempotency(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		created bool
		want    int
	}{{created: true, want: http.StatusCreated}, {created: false, want: http.StatusOK}} {
		h := newHandler(stubService{booking: func(c, hold uuid.UUID) (*domain.Booking, bool, error) {
			return &domain.Booking{ID: uuid.New(), HoldID: hold, CustomerID: c, Status: domain.StatusPendingPayment}, tt.created, nil
		}})

		rec := do(t, h, http.MethodPost, "/v1/bookings", fmt.Sprintf(`{"hold_id":%q}`, uuid.New()), uuid.New())

		if rec.Code != tt.want {
			t.Errorf("created=%v: status = %d, want %d", tt.created, rec.Code, tt.want)
		}
	}
}

func TestCancelBooking_Conflict(t *testing.T) {
	t.Parallel()
	h := newHandler(stubService{cancel: func(uuid.UUID, uuid.UUID) (*domain.Booking, error) {
		return nil, fmt.Errorf("cancel booking: %w: cannot cancel a confirmed booking", domain.ErrInvalidTransition)
	}})

	rec := do(t, h, http.MethodPost, "/v1/bookings/"+uuid.NewString()+"/cancel", "", uuid.New())

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}
