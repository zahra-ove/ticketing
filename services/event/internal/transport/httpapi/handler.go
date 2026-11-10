// Package httpapi exposes the event service over a JSON HTTP API.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// EventService is the subset of the application layer the API needs.
type EventService interface {
	CreateEvent(ctx context.Context, p domain.NewEventParams) (*domain.Event, error)
	GetEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	ListPublishedEvents(ctx context.Context, cursor string, limit int) (app.Page, error)
	PublishEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	CancelEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	ListSeats(ctx context.Context, eventID uuid.UUID) ([]domain.Seat, error)
}

type handler struct {
	svc EventService
	log *slog.Logger
}

// NewHandler returns the HTTP handler of the event service with all routes and
// middleware. db is checked by the readiness probe; *pgxpool.Pool satisfies it.
func NewHandler(svc EventService, db httpx.Pinger, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", httpx.Livez)
	mux.HandleFunc("GET /readyz", httpx.Readyz(log, map[string]httpx.Pinger{"database": db}))

	// Reading the catalog is public; changing it is for admins only.
	httpx.Route(mux, "GET /v1/events", h.listEvents)
	httpx.Route(mux, "GET /v1/events/{id}", h.getEvent)
	httpx.Route(mux, "GET /v1/events/{id}/seats", h.listSeats)
	httpx.Route(mux, "POST /v1/events", httpx.RequireRole(h.createEvent, httpx.RoleAdmin))
	httpx.Route(mux, "POST /v1/events/{id}/publish", httpx.RequireRole(h.publishEvent, httpx.RoleAdmin))
	httpx.Route(mux, "POST /v1/events/{id}/cancel", httpx.RequireRole(h.cancelEvent, httpx.RoleAdmin))

	return httpx.Middleware(mux, log)
}

// writeError maps an error to an HTTP problem response. Unknown errors become
// 500 and are logged with their full chain; their details never reach the client.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		verr *domain.ValidationError
		berr *httpx.BadRequestError
	)
	switch {
	case errors.As(err, &berr):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: berr.Msg})
	case errors.As(err, &verr):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "request validation failed", Errors: verr.Fields})
	case errors.Is(err, domain.ErrNotFound):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Detail: domain.ErrNotFound.Error()})
	case errors.Is(err, domain.ErrInvalidTransition):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusConflict, Detail: err.Error()})
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

func (h *handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ev, err := h.svc.CreateEvent(r.Context(), req.toParams())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Location", "/v1/events/"+ev.ID.String())
	httpx.WriteJSON(w, http.StatusCreated, toEventResponse(ev))
}

func (h *handler) getEvent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	ev, err := h.svc.GetEvent(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toEventResponse(ev))
}

func (h *handler) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 0
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			h.writeError(w, r, httpx.BadRequest("limit must be a positive integer"))
			return
		}
		limit = n
	}

	page, err := h.svc.ListPublishedEvents(r.Context(), q.Get("cursor"), limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	resp := listEventsResponse{Data: make([]eventResponse, len(page.Events)), NextCursor: page.NextCursor}
	for i := range page.Events {
		resp.Data[i] = toEventResponse(&page.Events[i])
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) publishEvent(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.PublishEvent)
}

func (h *handler) cancelEvent(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.CancelEvent)
}

func (h *handler) transition(w http.ResponseWriter, r *http.Request, fn func(context.Context, uuid.UUID) (*domain.Event, error)) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	ev, err := fn(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toEventResponse(ev))
}

func (h *handler) listSeats(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	seats, err := h.svc.ListSeats(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listSeatsResponse{Data: toSeatResponses(seats)})
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("id must be a valid UUID")
	}
	return id, nil
}
