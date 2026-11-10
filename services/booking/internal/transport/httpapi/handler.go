// Package httpapi exposes the booking service over a JSON HTTP API.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// BookingService is the subset of the application layer the API needs.
type BookingService interface {
	HoldSeats(ctx context.Context, customerID, eventID uuid.UUID, seatIDs []uuid.UUID) (*domain.Hold, error)
	GetHold(ctx context.Context, customerID, holdID uuid.UUID) (*domain.Hold, error)
	ReleaseHold(ctx context.Context, customerID, holdID uuid.UUID) error
	CreateBooking(ctx context.Context, customerID, holdID uuid.UUID) (*domain.Booking, bool, error)
	GetBooking(ctx context.Context, customerID, id uuid.UUID) (*domain.Booking, error)
	CancelBooking(ctx context.Context, customerID, id uuid.UUID) (*domain.Booking, error)
	GetAvailability(ctx context.Context, eventID uuid.UUID) (app.Availability, error)
}

type handler struct {
	svc BookingService
	log *slog.Logger
}

// NewHandler returns the HTTP handler of the booking service. deps are
// checked by the readiness probe.
func NewHandler(svc BookingService, deps map[string]httpx.Pinger, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", httpx.Livez)
	mux.HandleFunc("GET /readyz", httpx.Readyz(log, deps))

	httpx.Route(mux, "GET /v1/events/{id}/availability", h.availability)

	httpx.Route(mux, "POST /v1/holds", h.withCustomer(h.holdSeats))
	httpx.Route(mux, "GET /v1/holds/{id}", h.withCustomer(h.getHold))
	httpx.Route(mux, "DELETE /v1/holds/{id}", h.withCustomer(h.releaseHold))

	httpx.Route(mux, "POST /v1/bookings", h.withCustomer(h.createBooking))
	httpx.Route(mux, "GET /v1/bookings/{id}", h.withCustomer(h.getBooking))
	httpx.Route(mux, "POST /v1/bookings/{id}/cancel", h.withCustomer(h.cancelBooking))

	return httpx.Middleware(mux, log)
}

type customerHandler func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID)

// withCustomer requires an authenticated caller. The caller's id comes from
// the identity headers that the gateway sets from a verified token.
func (h *handler) withCustomer(next customerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := httpx.CallerFrom(r)
		if !ok {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "a valid bearer token is required"})
			return
		}
		next(w, r, c.ID)
	}
}

// writeError maps an error to an HTTP problem response.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		berr *httpx.BadRequestError
		verr *domain.ValidationError
		uerr *domain.SeatsUnavailableError
	)
	switch {
	case errors.As(err, &berr):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: berr.Msg})
	case errors.As(err, &verr):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "request validation failed", Errors: verr.Fields})
	case errors.As(err, &uerr):
		httpx.WriteProblem(w, r, httpx.Problem{
			Status: http.StatusConflict,
			Title:  "Seats unavailable",
			Detail: "some seats are held or booked by someone else",
			Extra:  map[string]any{"seat_ids": uerr.SeatIDs},
		})
	case errors.Is(err, domain.ErrHoldNotFound), errors.Is(err, domain.ErrBookingNotFound), errors.Is(err, domain.ErrEventNotFound):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Detail: rootMessage(err)})
	case errors.Is(err, domain.ErrEventNotBookable), errors.Is(err, domain.ErrInvalidTransition):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusConflict, Detail: rootMessage(err)})
	case errors.Is(err, app.ErrDependencyUnavailable):
		w.Header().Set("Retry-After", "1")
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusServiceUnavailable, Detail: "a dependency is temporarily unavailable; retry shortly"})
	default:
		h.log.ErrorContext(r.Context(), "request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("request_id", httpx.RequestIDFrom(r.Context())),
			slog.Any("error", err),
		)
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
	}
}

// rootMessage returns the message of the domain sentinel in err, without the
// wrapping context. Invalid transitions keep their message, which explains
// the current status ("cannot cancel a confirmed booking").
func rootMessage(err error) string {
	for _, target := range []error{
		domain.ErrHoldNotFound, domain.ErrBookingNotFound, domain.ErrEventNotFound, domain.ErrEventNotBookable,
	} {
		if errors.Is(err, target) {
			return target.Error()
		}
	}
	return err.Error()
}

func (h *handler) availability(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	av, err := h.svc.GetAvailability(r.Context(), eventID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAvailabilityResponse(eventID, av))
}

func (h *handler) holdSeats(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	var req holdSeatsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	hold, err := h.svc.HoldSeats(r.Context(), customerID, req.EventID, req.SeatIDs)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/holds/"+hold.ID.String())
	httpx.WriteJSON(w, http.StatusCreated, toHoldResponse(hold))
}

func (h *handler) getHold(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	hold, err := h.svc.GetHold(r.Context(), customerID, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toHoldResponse(hold))
}

func (h *handler) releaseHold(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.ReleaseHold(r.Context(), customerID, id); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) createBooking(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	var req createBookingRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	b, created, err := h.svc.CreateBooking(r.Context(), customerID, req.HoldID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// 201 for a new booking, 200 when a retry returns the existing one.
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", "/v1/bookings/"+b.ID.String())
	httpx.WriteJSON(w, status, toBookingResponse(b))
}

func (h *handler) getBooking(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	b, err := h.svc.GetBooking(r.Context(), customerID, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toBookingResponse(b))
}

func (h *handler) cancelBooking(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	b, err := h.svc.CancelBooking(r.Context(), customerID, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toBookingResponse(b))
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("id must be a valid UUID")
	}
	return id, nil
}
