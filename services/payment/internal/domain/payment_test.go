package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func charge() domain.Charge {
	return domain.Charge{BookingID: uuid.New(), CustomerID: uuid.New(), AmountCents: 5000}
}

func TestNewSucceeded_RecordsEvent(t *testing.T) {
	t.Parallel()
	p, err := domain.NewSucceeded(charge(), "ch_123", now)
	if err != nil {
		t.Fatal(err)
	}
	events := p.PullEvents()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if ev, ok := events[0].(domain.PaymentSucceeded); !ok || ev.Payment.ProviderRef != "ch_123" {
		t.Errorf("event = %#v", events[0])
	}
}

func TestRefund(t *testing.T) {
	t.Parallel()

	ok, _ := domain.NewSucceeded(charge(), "ch_1", now)
	ok.PullEvents()
	if err := ok.Refund(now); err != nil {
		t.Fatal(err)
	}
	if ok.Status != domain.StatusRefunded {
		t.Errorf("status = %s", ok.Status)
	}
	if _, isRefund := ok.PullEvents()[0].(domain.PaymentRefunded); !isRefund {
		t.Error("refund must record PaymentRefunded")
	}
	if err := ok.Refund(now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("second refund error = %v", err)
	}

	failed, _ := domain.NewFailed(charge(), "card_declined", now)
	if err := failed.Refund(now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("refund of a failed payment error = %v", err)
	}
}
