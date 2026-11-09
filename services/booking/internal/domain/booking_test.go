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
		if err := b.Cancel(now); err != nil {
			t.Fatal(err)
		}
		if b.Active() {
			t.Error("cancelled booking must release its seats")
		}
		if err := b.Cancel(now); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("second cancel error = %v", err)
		}
	})

	t.Run("confirmed cannot be cancelled", func(t *testing.T) {
		t.Parallel()
		b := newPending(t)
		if err := b.Confirm(now); err != nil {
			t.Fatal(err)
		}
		if err := b.Cancel(now); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("cancel confirmed error = %v", err)
		}
	})
}
