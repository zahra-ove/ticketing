package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/payment/internal/app"
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

type fakeProvider struct {
	mu        sync.Mutex
	charges   map[string]string // idempotency key -> reference
	refunds   map[string]bool
	declineAt int64 // amounts equal to this are declined
	transient int   // number of calls that fail with a transient error first
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{charges: map[string]string{}, refunds: map[string]bool{}, declineAt: -1}
}

func (f *fakeProvider) Charge(_ context.Context, req app.ChargeRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transient > 0 {
		f.transient--
		return "", errors.New("provider timeout")
	}
	if req.AmountCents == f.declineAt {
		return "", &app.DeclinedError{Reason: "card_declined"}
	}
	if ref, ok := f.charges[req.IdempotencyKey]; ok {
		return ref, nil
	}
	ref := "ch_" + uuid.NewString()
	f.charges[req.IdempotencyKey] = ref
	return ref, nil
}

func (f *fakeProvider) Refund(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds[key] = true
	return nil
}

type fakeRepo struct {
	mu       sync.Mutex
	payments map[uuid.UUID]*domain.Payment // by booking id
	events   []domain.Event
}

func newFakeRepo() *fakeRepo { return &fakeRepo{payments: map[uuid.UUID]*domain.Payment{}} }

func (r *fakeRepo) Create(_ context.Context, p *domain.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.payments[p.BookingID]; ok {
		return app.ErrDuplicatePayment
	}
	r.events = append(r.events, p.PullEvents()...)
	cp := *p
	r.payments[p.BookingID] = &cp
	return nil
}

func (r *fakeRepo) GetByBookingID(_ context.Context, bookingID uuid.UUID) (*domain.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.payments[bookingID]
	if !ok {
		return nil, domain.ErrPaymentNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *fakeRepo) Update(_ context.Context, id uuid.UUID, fn func(*domain.Payment) error) (*domain.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, p := range r.payments {
		if p.ID != id {
			continue
		}
		cp := *p
		if err := fn(&cp); err != nil {
			return nil, err
		}
		r.events = append(r.events, cp.PullEvents()...)
		r.payments[k] = &cp
		return &cp, nil
	}
	return nil, domain.ErrPaymentNotFound
}

func setup() (*app.Service, *fakeProvider, *fakeRepo) {
	prov, repo := newFakeProvider(), newFakeRepo()
	return app.NewService(prov, repo, app.WithClock(func() time.Time { return now })), prov, repo
}

func charge(amount int64) domain.Charge {
	return domain.Charge{BookingID: uuid.New(), CustomerID: uuid.New(), AmountCents: amount}
}

func TestChargeBooking_Succeeds(t *testing.T) {
	t.Parallel()
	svc, prov, repo := setup()
	c := charge(5000)

	// A redelivered BookingCreated must not charge twice.
	for i := range 3 {
		_, charged, err := svc.ChargeBooking(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if charged != (i == 0) {
			t.Errorf("delivery %d: charged = %v", i+1, charged)
		}
	}

	p, err := svc.GetByBookingID(t.Context(), c.BookingID)
	if err != nil || p.Status != domain.StatusSucceeded {
		t.Fatalf("payment = %+v, %v", p, err)
	}
	if len(prov.charges) != 1 || len(repo.events) != 1 {
		t.Errorf("got %d charges and %d events, want 1 each", len(prov.charges), len(repo.events))
	}
}

func TestChargeBooking_DeclinedBecomesFailedPayment(t *testing.T) {
	t.Parallel()
	svc, prov, repo := setup()
	prov.declineAt = 1313
	c := charge(1313)

	if _, _, err := svc.ChargeBooking(t.Context(), c); err != nil {
		t.Fatalf("a decline is a result, not an error: %v", err)
	}
	p, _ := svc.GetByBookingID(t.Context(), c.BookingID)
	if p.Status != domain.StatusFailed || p.FailureReason != "card_declined" {
		t.Errorf("payment = %s/%s", p.Status, p.FailureReason)
	}
	if _, ok := repo.events[0].(domain.PaymentFailed); !ok {
		t.Errorf("event = %#v, want PaymentFailed", repo.events[0])
	}
}

func TestChargeBooking_TransientErrorIsReturnedForRetry(t *testing.T) {
	t.Parallel()
	svc, prov, _ := setup()
	prov.transient = 1
	c := charge(5000)

	if _, _, err := svc.ChargeBooking(t.Context(), c); err == nil {
		t.Fatal("transient provider error must be returned so the consumer retries")
	}
	if _, err := svc.GetByBookingID(t.Context(), c.BookingID); !errors.Is(err, domain.ErrPaymentNotFound) {
		t.Errorf("no payment may be stored after a transient error, got %v", err)
	}
	if _, _, err := svc.ChargeBooking(t.Context(), c); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestRefundBooking_IsIdempotent(t *testing.T) {
	t.Parallel()
	svc, prov, repo := setup()
	c := charge(5000)
	if _, _, err := svc.ChargeBooking(t.Context(), c); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := svc.RefundBooking(t.Context(), c.BookingID); err != nil {
			t.Fatal(err)
		}
	}

	p, _ := svc.GetByBookingID(t.Context(), c.BookingID)
	if p.Status != domain.StatusRefunded {
		t.Errorf("status = %s, want refunded", p.Status)
	}
	if len(prov.refunds) != 1 || len(repo.events) != 2 {
		t.Errorf("got %d refunds and %d events, want 1 refund and succeeded+refunded", len(prov.refunds), len(repo.events))
	}
}
