package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestValidateSeatSelection(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	tooMany := make([]uuid.UUID, domain.MaxSeatsPerHold+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}

	tests := []struct {
		name    string
		ids     []uuid.UUID
		wantErr bool
	}{
		{name: "one seat", ids: []uuid.UUID{id}},
		{name: "empty", ids: nil, wantErr: true},
		{name: "too many", ids: tooMany, wantErr: true},
		{name: "duplicates", ids: []uuid.UUID{id, id}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateSeatSelection(tt.ids)
			var verr *domain.ValidationError
			if tt.wantErr != errors.As(err, &verr) {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewBooking_FromHold(t *testing.T) {
	t.Parallel()

	hold := &domain.Hold{
		ID:         uuid.New(),
		EventID:    uuid.New(),
		CustomerID: uuid.New(),
		Seats: []domain.Seat{
			{ID: uuid.New(), Label: "A-1-1", PriceCents: 1500},
			{ID: uuid.New(), Label: "A-1-2", PriceCents: 2500},
		},
	}

	b, err := domain.NewBooking(hold, now)
	if err != nil {
		t.Fatal(err)
	}

	if b.Status != domain.StatusPendingPayment || b.TotalCents != 4000 || b.HoldID != hold.ID {
		t.Errorf("booking = %+v", b)
	}
	if !b.Active() {
		t.Error("new booking should block its seats")
	}
}

func TestBooking_Transitions(t *testing.T) {
	t.Parallel()

	newPending := func(t *testing.T) *domain.Booking {
		t.Helper()
		b, err := domain.NewBooking(&domain.Hold{ID: uuid.New()}, now)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("cancel pending", func(t *testing.T) {
		t.Parallel()
		b := newPending(t)
		if err := b.Cancel(now, domain.CancelByCustomer); err != nil {
			t.Fatal(err)
		}
		if b.Active() {
			t.Error("cancelled booking must release its seats")
		}
		if err := b.Cancel(now, domain.CancelByCustomer); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("second cancel error = %v", err)
		}
	})

	t.Run("confirmed cannot be cancelled", func(t *testing.T) {
		t.Parallel()
		b := newPending(t)
		if err := b.Confirm(now); err != nil {
			t.Fatal(err)
		}
		if err := b.Cancel(now, domain.CancelByCustomer); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("cancel confirmed error = %v", err)
		}
	})
}

func TestBooking_RecordsEvents(t *testing.T) {
	t.Parallel()

	b, err := domain.NewBooking(&domain.Hold{ID: uuid.New(), Seats: []domain.Seat{{ID: uuid.New(), PriceCents: 700}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	created := b.PullEvents()
	if len(created) != 1 {
		t.Fatalf("got %d events after NewBooking, want 1", len(created))
	}
	if ev, ok := created[0].(domain.BookingCreated); !ok || ev.Booking.TotalCents != 700 {
		t.Errorf("event = %#v, want BookingCreated with total 700", created[0])
	}
	if len(b.PullEvents()) != 0 {
		t.Error("PullEvents must forget returned events")
	}

	if err := b.Cancel(now, domain.CancelPaymentTimeout); err != nil {
		t.Fatal(err)
	}
	paymentID := uuid.New()
	if err := b.RequestRefund(paymentID, now); err != nil {
		t.Fatal(err)
	}

	events := b.PullEvents()
	if len(events) != 2 {
		t.Fatalf("got %d events, want cancelled + refund", len(events))
	}
	if ev, ok := events[0].(domain.BookingCancelled); !ok || ev.Reason != domain.CancelPaymentTimeout {
		t.Errorf("first event = %#v", events[0])
	}
	if ev, ok := events[1].(domain.RefundRequested); !ok || ev.PaymentID != paymentID {
		t.Errorf("second event = %#v", events[1])
	}
}

func TestBooking_RefundOnlyWhenCancelled(t *testing.T) {
	t.Parallel()
	b, _ := domain.NewBooking(&domain.Hold{ID: uuid.New()}, now)

	if err := b.RequestRefund(uuid.New(), now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("refund of a pending booking: error = %v, want ErrInvalidTransition", err)
	}
}
