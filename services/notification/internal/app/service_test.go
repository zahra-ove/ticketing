package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/notification/internal/app"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []app.Email
	err  error
}

func (m *fakeMailer) Send(_ context.Context, e app.Email) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, e)
	return nil
}

// fakeInbox records a message as processed only when fn succeeds, like the real one.
type fakeInbox struct {
	mu   sync.Mutex
	done map[uuid.UUID]bool
}

func (i *fakeInbox) Once(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.done[id] {
		return nil
	}
	if err := fn(ctx); err != nil {
		return err
	}
	i.done[id] = true
	return nil
}

func setup() (*app.Service, *fakeMailer) {
	m := &fakeMailer{}
	return app.NewService(m, &fakeInbox{done: map[uuid.UUID]bool{}}, app.ExampleDirectory), m
}

func TestNotifyBookingConfirmed(t *testing.T) {
	t.Parallel()
	svc, mailer := setup()
	customer, booking := uuid.New(), uuid.New()

	err := svc.NotifyBookingConfirmed(t.Context(), uuid.New(), app.BookingConfirmed{BookingID: booking, CustomerID: customer, AmountCents: 5050})
	if err != nil {
		t.Fatal(err)
	}

	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(mailer.sent))
	}
	e := mailer.sent[0]
	if e.To != app.ExampleDirectory(customer) || !strings.Contains(e.Body, "50.50") || !strings.Contains(e.Body, booking.String()) {
		t.Errorf("email = %+v", e)
	}
}

func TestNotify_SameMessageTwiceSendsOnce(t *testing.T) {
	t.Parallel()
	svc, mailer := setup()
	msgID := uuid.New()
	d := app.BookingCancelled{BookingID: uuid.New(), CustomerID: uuid.New(), Reason: "payment_timeout"}

	for range 3 {
		if err := svc.NotifyBookingCancelled(t.Context(), msgID, d); err != nil {
			t.Fatal(err)
		}
	}

	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d emails for one message, want 1", len(mailer.sent))
	}
	if !strings.Contains(mailer.sent[0].Body, "did not arrive in time") {
		t.Errorf("body does not explain the timeout: %q", mailer.sent[0].Body)
	}
}

func TestNotify_FailedSendIsRetried(t *testing.T) {
	t.Parallel()
	svc, mailer := setup()
	msgID := uuid.New()
	d := app.PaymentRefunded{BookingID: uuid.New(), CustomerID: uuid.New(), AmountCents: 1000}

	mailer.err = errors.New("smtp down")
	if err := svc.NotifyPaymentRefunded(t.Context(), msgID, d); err == nil {
		t.Fatal("a failed send must return an error so the consumer retries")
	}

	mailer.err = nil
	if err := svc.NotifyPaymentRefunded(t.Context(), msgID, d); err != nil {
		t.Fatal(err)
	}
	if len(mailer.sent) != 1 {
		t.Errorf("sent %d emails, want 1 after the retry", len(mailer.sent))
	}
}
