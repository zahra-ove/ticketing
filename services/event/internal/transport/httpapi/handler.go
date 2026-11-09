// Package httpapi exposes the event service over a JSON HTTP API.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"

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

	route(mux, "POST /v1/events", h.createEvent)
	route(mux, "GET /v1/events", h.listEvents)
	route(mux, "GET /v1/events/{id}", h.getEvent)
	route(mux, "POST /v1/events/{id}/publish", h.publishEvent)
	route(mux, "POST /v1/events/{id}/cancel", h.cancelEvent)
	route(mux, "GET /v1/events/{id}/seats", h.listSeats)

	// Order matters (outermost last): otelhttp starts the span first, so the
	// request id, access log and recoverer all run inside it and can log the
	// trace id; recoverer is innermost so the access log records the 500.
	var hdl http.Handler = mux
	hdl = recoverer(log)(hdl)
	hdl = accessLog(log)(hdl)
	hdl = requestID(hdl)
	hdl = otelhttp.NewHandler(hdl, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/livez" && r.URL.Path != "/readyz" // probes are noise
		}),
	)
	return hdl
}

// route registers fn for pattern and names the span and metrics after the
// route template ("GET /v1/events/{id}"), never the raw path, which would
// explode metric cardinality with one series per event id.
func route(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	_, path, _ := strings.Cut(pattern, " ")
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		span.SetName(pattern)
		span.SetAttributes(semconv.HTTPRoute(path))
		if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			l.Add(semconv.HTTPRoute(path))
		}
		fn(w, r)
	}))
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
