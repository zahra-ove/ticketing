// Package httpapi exposes the event service over a JSON HTTP API.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

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

// Pinger reports whether a dependency is reachable. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

type handler struct {
	svc EventService
	db  Pinger
	log *slog.Logger
}

// NewHandler returns the HTTP handler of the event service with all routes and middleware.
func NewHandler(svc EventService, db Pinger, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, db: db, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", h.livez)
	mux.HandleFunc("GET /readyz", h.readyz)

	mux.HandleFunc("POST /v1/events", h.createEvent)
	mux.HandleFunc("GET /v1/events", h.listEvents)
	mux.HandleFunc("GET /v1/events/{id}", h.getEvent)
	mux.HandleFunc("POST /v1/events/{id}/publish", h.publishEvent)
	mux.HandleFunc("POST /v1/events/{id}/cancel", h.cancelEvent)
	mux.HandleFunc("GET /v1/events/{id}/seats", h.listSeats)

	// Order matters: request id first so every later layer can log it,
	// recoverer innermost so the access log records the 500.
	var hdl http.Handler = mux
	hdl = recoverer(log)(hdl)
	hdl = accessLog(log)(hdl)
	hdl = requestID(hdl)
	return hdl
}

// livez reports that the process is up. It never checks dependencies, so a
// database outage does not make the orchestrator restart healthy containers.
func (h *handler) livez(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports whether the service can serve traffic right now.
func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		h.log.WarnContext(ctx, "readiness check failed", slog.Any("error", err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "up"})
}

func (h *handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ev, err := h.svc.CreateEvent(r.Context(), req.toParams())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Location", "/v1/events/"+ev.ID.String())
	writeJSON(w, http.StatusCreated, toEventResponse(ev))
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
	writeJSON(w, http.StatusOK, toEventResponse(ev))
}

func (h *handler) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 0
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			h.writeError(w, r, badRequest{"limit must be a positive integer"})
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
	writeJSON(w, http.StatusOK, resp)
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
	writeJSON(w, http.StatusOK, toEventResponse(ev))
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
	writeJSON(w, http.StatusOK, listSeatsResponse{Data: toSeatResponses(seats)})
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, badRequest{"id must be a valid UUID"}
	}
	return id, nil
}
