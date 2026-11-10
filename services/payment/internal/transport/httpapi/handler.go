// Package httpapi exposes payments for reading over HTTP. Payments are
// created by the saga (Kafka), never by an HTTP call.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

// Service is the subset of the application layer the API needs.
type Service interface {
	GetByBookingID(ctx context.Context, bookingID uuid.UUID) (*domain.Payment, error)
}

type paymentResponse struct {
	ID            uuid.UUID `json:"id"`
	BookingID     uuid.UUID `json:"booking_id"`
	AmountCents   int64     `json:"amount_cents"`
	Status        string    `json:"status"`
	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// NewHandler returns the HTTP handler of the payment service.
func NewHandler(svc Service, deps map[string]httpx.Pinger, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", httpx.Livez)
	mux.HandleFunc("GET /readyz", httpx.Readyz(log, deps))

	httpx.Route(mux, "GET /v1/payments", httpx.RequireRole(func(w http.ResponseWriter, r *http.Request) {
		caller, _ := httpx.CallerFrom(r)
		bookingID, err := uuid.Parse(r.URL.Query().Get("booking_id"))
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: "booking_id query parameter must be a valid UUID"})
			return
		}
		p, err := svc.GetByBookingID(r.Context(), bookingID)
		// Customers see only their own payments; someone else's looks like a missing one.
		if err == nil && !caller.IsAdmin() && p.CustomerID != caller.ID {
			err = domain.ErrPaymentNotFound
		}
		switch {
		case errors.Is(err, domain.ErrPaymentNotFound):
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Detail: "no payment for this booking yet"})
			return
		case err != nil:
			log.ErrorContext(r.Context(), "get payment failed", slog.Any("error", err))
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, paymentResponse{
			ID: p.ID, BookingID: p.BookingID, AmountCents: p.AmountCents, Status: string(p.Status),
			FailureReason: p.FailureReason, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		})
	}, httpx.RoleCustomer, httpx.RoleAdmin))

	return httpx.Middleware(mux, log)
}
