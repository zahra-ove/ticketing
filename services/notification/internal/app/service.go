// Package app decides which email a customer gets for each booking and
// payment event, and makes sure each event produces at most one email.
package app

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"text/template"
	"time"

	"github.com/google/uuid"
)

// Email is a message ready to be sent.
type Email struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends emails.
type Mailer interface {
	Send(ctx context.Context, e Email) error
}

// Inbox deduplicates incoming messages (the inbox pattern). Once runs fn only
// if messageID was never processed successfully, and records it atomically
// with fn's success. Kafka delivers at least once; the inbox turns that into
// "effectively once" for side effects such as sending an email.
type Inbox interface {
	Once(ctx context.Context, messageID uuid.UUID, fn func(ctx context.Context) error) error
}

// Directory resolves a customer's email address. Until there is a customer
// service, addresses are derived from the id.
type Directory func(customerID uuid.UUID) string

// ExampleDirectory gives every customer a test address that Mailpit catches.
func ExampleDirectory(customerID uuid.UUID) string {
	return "customer-" + customerID.String() + "@example.test"
}

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"money": func(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) },
	"short": func(id uuid.UUID) string { return id.String()[:8] },
}).ParseFS(templateFS, "templates/*.tmpl"))

// Service sends booking notifications.
type Service struct {
	mailer    Mailer
	inbox     Inbox
	directory Directory
}

// NewService returns a Service.
func NewService(mailer Mailer, inbox Inbox, directory Directory) *Service {
	return &Service{mailer: mailer, inbox: inbox, directory: directory}
}

// BookingConfirmed is the data of a confirmation email.
type BookingConfirmed struct {
	BookingID   uuid.UUID
	CustomerID  uuid.UUID
	AmountCents int64
	ConfirmedAt time.Time
}

// BookingCancelled is the data of a cancellation email.
type BookingCancelled struct {
	BookingID  uuid.UUID
	CustomerID uuid.UUID
	// Reason is one of customer, payment_failed, payment_timeout.
	Reason string
}

// PaymentRefunded is the data of a refund email.
type PaymentRefunded struct {
	BookingID   uuid.UUID
	CustomerID  uuid.UUID
	AmountCents int64
}

// NotifyBookingConfirmed sends the booking confirmation.
func (s *Service) NotifyBookingConfirmed(ctx context.Context, messageID uuid.UUID, d BookingConfirmed) error {
	return s.send(ctx, messageID, d.CustomerID, "Your booking is confirmed", "booking_confirmed.tmpl", d)
}

// NotifyBookingCancelled tells the customer their booking was cancelled and why.
func (s *Service) NotifyBookingCancelled(ctx context.Context, messageID uuid.UUID, d BookingCancelled) error {
	return s.send(ctx, messageID, d.CustomerID, "Your booking was cancelled", "booking_cancelled.tmpl", d)
}

// NotifyPaymentRefunded tells the customer their money is on its way back.
func (s *Service) NotifyPaymentRefunded(ctx context.Context, messageID uuid.UUID, d PaymentRefunded) error {
	return s.send(ctx, messageID, d.CustomerID, "Your payment was refunded", "payment_refunded.tmpl", d)
}

func (s *Service) send(ctx context.Context, messageID, customerID uuid.UUID, subject, tmpl string, data any) error {
	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, tmpl, data); err != nil {
		return fmt.Errorf("render %s: %w", tmpl, err)
	}
	email := Email{To: s.directory(customerID), Subject: subject, Body: body.String()}

	return s.inbox.Once(ctx, messageID, func(ctx context.Context) error {
		if err := s.mailer.Send(ctx, email); err != nil {
			return fmt.Errorf("send %q to %s: %w", subject, email.To, err)
		}
		return nil
	})
}
