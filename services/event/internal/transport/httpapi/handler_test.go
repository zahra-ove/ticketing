package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
	"github.com/zahra-ove/ticketing/services/event/internal/transport/httpapi"
)

// stubService lets each test decide what the application layer returns.
type stubService struct {
	create  func(domain.NewEventParams) (*domain.Event, error)
	get     func(uuid.UUID) (*domain.Event, error)
	list    func(cursor string, limit int) (app.Page, error)
	publish func(uuid.UUID) (*domain.Event, error)
	seats   func(uuid.UUID) ([]domain.Seat, error)
}

func (s stubService) CreateEvent(_ context.Context, p domain.NewEventParams) (*domain.Event, error) {
	return s.create(p)
}

func (s stubService) GetEvent(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	return s.get(id)
}

func (s stubService) ListPublishedEvents(_ context.Context, cursor string, limit int) (app.Page, error) {
	return s.list(cursor, limit)
}

func (s stubService) PublishEvent(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	return s.publish(id)
}

func (s stubService) CancelEvent(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	return s.publish(id)
}

func (s stubService) ListSeats(_ context.Context, id uuid.UUID) ([]domain.Seat, error) {
	return s.seats(id)
}

type stubPinger struct{ err error }

func (p stubPinger) Ping(context.Context) error { return p.err }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func sampleEvent() *domain.Event {
	t := time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC)
	return &domain.Event{
		ID:       uuid.MustParse("01930000-0000-7000-8000-000000000001"),
		Title:    "Go Meetup",
		Venue:    "Hall 1",
		StartsAt: t,
		Status:   domain.StatusDraft,
		Sections: []domain.Section{{Name: "A", Rows: 2, SeatsPerRow: 5, PriceCents: 1000}},
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestCreateEvent(t *testing.T) {
	t.Parallel()

	validBody := `{"title":"Go Meetup","venue":"Hall 1","starts_at":"2026-12-01T18:00:00Z",
		"sections":[{"name":"A","rows":2,"seats_per_row":5,"price_cents":1000}]}`

	tests := []struct {
		name       string
		body       string
		svcErr     error
		wantStatus int
	}{
		{name: "created", body: validBody, wantStatus: http.StatusCreated},
		{name: "empty body", body: "", wantStatus: http.StatusBadRequest},
		{name: "malformed json", body: `{"title":`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"titel":"x"}`, wantStatus: http.StatusBadRequest},
		{name: "wrong type", body: `{"title":42}`, wantStatus: http.StatusBadRequest},
		{name: "two objects", body: validBody + validBody, wantStatus: http.StatusBadRequest},
		{
			name:       "validation error",
			body:       validBody,
			svcErr:     &domain.ValidationError{Fields: map[string]string{"title": "must not be empty"}},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{name: "unexpected error", body: validBody, svcErr: errors.New("db down"), wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := stubService{create: func(p domain.NewEventParams) (*domain.Event, error) {
				if tt.svcErr != nil {
					return nil, tt.svcErr
				}
				if p.Title != "Go Meetup" || len(p.Sections) != 1 || p.Sections[0].SeatsPerRow != 5 {
					t.Errorf("unexpected params: %+v", p)
				}
				return sampleEvent(), nil
			}}
			h := httpapi.NewHandler(svc, stubPinger{}, discard)

			rec := do(t, h, http.MethodPost, "/v1/events", tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if rec.Code == http.StatusCreated {
				if loc := rec.Header().Get("Location"); loc != "/v1/events/"+sampleEvent().ID.String() {
					t.Errorf("Location = %q", loc)
				}
				if got := decode[map[string]any](t, rec)["capacity"]; got != float64(10) {
					t.Errorf("capacity = %v, want 10", got)
				}
			}
			if rec.Code >= 400 && rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("error Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestCreateEvent_ValidationErrorListsFields(t *testing.T) {
	t.Parallel()

	svc := stubService{create: func(domain.NewEventParams) (*domain.Event, error) {
		return nil, &domain.ValidationError{Fields: map[string]string{"venue": "must not be empty"}}
	}}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	rec := do(t, h, http.MethodPost, "/v1/events", `{"title":"x"}`)

	body := decode[struct {
		Status    int               `json:"status"`
		Errors    map[string]string `json:"errors"`
		RequestID string            `json:"request_id"`
	}](t, rec)
	if body.Errors["venue"] != "must not be empty" {
		t.Errorf("errors = %v", body.Errors)
	}
	if body.RequestID == "" || body.RequestID != rec.Header().Get(httpapi.RequestIDHeader) {
		t.Errorf("request_id = %q, header = %q", body.RequestID, rec.Header().Get(httpapi.RequestIDHeader))
	}
}

func TestGetEvent(t *testing.T) {
	t.Parallel()

	ev := sampleEvent()
	svc := stubService{get: func(id uuid.UUID) (*domain.Event, error) {
		if id == ev.ID {
			return ev, nil
		}
		return nil, domain.ErrNotFound
	}}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "found", path: "/v1/events/" + ev.ID.String(), wantStatus: http.StatusOK},
		{name: "not found", path: "/v1/events/" + uuid.NewString(), wantStatus: http.StatusNotFound},
		{name: "invalid id", path: "/v1/events/not-a-uuid", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if rec := do(t, h, http.MethodGet, tt.path, ""); rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestPublishEvent_Conflict(t *testing.T) {
	t.Parallel()

	svc := stubService{publish: func(uuid.UUID) (*domain.Event, error) {
		return nil, domain.ErrInvalidTransition
	}}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	rec := do(t, h, http.MethodPost, "/v1/events/"+uuid.NewString()+"/publish", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestListEvents(t *testing.T) {
	t.Parallel()

	var gotCursor string
	var gotLimit int
	svc := stubService{list: func(cursor string, limit int) (app.Page, error) {
		gotCursor, gotLimit = cursor, limit
		return app.Page{Events: []domain.Event{*sampleEvent()}, NextCursor: "next"}, nil
	}}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	rec := do(t, h, http.MethodGet, "/v1/events?limit=5&cursor=abc", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotCursor != "abc" || gotLimit != 5 {
		t.Errorf("service called with cursor=%q limit=%d", gotCursor, gotLimit)
	}
	body := decode[struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}](t, rec)
	if len(body.Data) != 1 || body.NextCursor != "next" {
		t.Errorf("body = %+v", body)
	}

	if rec := do(t, h, http.MethodGet, "/v1/events?limit=zero", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid limit status = %d, want 400", rec.Code)
	}
}

func TestListSeats(t *testing.T) {
	t.Parallel()

	ev := sampleEvent()
	seats, err := ev.GenerateSeats()
	if err != nil {
		t.Fatal(err)
	}
	svc := stubService{seats: func(uuid.UUID) ([]domain.Seat, error) { return seats, nil }}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	rec := do(t, h, http.MethodGet, "/v1/events/"+ev.ID.String()+"/seats", "")

	body := decode[struct {
		Data []struct {
			Label string `json:"label"`
		} `json:"data"`
	}](t, rec)
	if len(body.Data) != 10 || body.Data[0].Label != "A-1-1" {
		t.Errorf("seats = %+v", body.Data)
	}
}

func TestHealth(t *testing.T) {
	t.Parallel()

	up := httpapi.NewHandler(stubService{}, stubPinger{}, discard)
	down := httpapi.NewHandler(stubService{}, stubPinger{err: errors.New("connection refused")}, discard)

	if rec := do(t, down, http.MethodGet, "/livez", ""); rec.Code != http.StatusOK {
		t.Errorf("livez with db down = %d, want 200", rec.Code)
	}
	if rec := do(t, up, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
		t.Errorf("readyz with db up = %d, want 200", rec.Code)
	}
	if rec := do(t, down, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz with db down = %d, want 503", rec.Code)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	t.Parallel()

	svc := stubService{get: func(uuid.UUID) (*domain.Event, error) { panic("boom") }}
	h := httpapi.NewHandler(svc, stubPinger{}, discard)

	rec := do(t, h, http.MethodGet, "/v1/events/"+uuid.NewString(), "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRequestID_IsPropagated(t *testing.T) {
	t.Parallel()

	h := httpapi.NewHandler(stubService{}, stubPinger{}, discard)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	req.Header.Set(httpapi.RequestIDHeader, "abc-123")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(httpapi.RequestIDHeader); got != "abc-123" {
		t.Errorf("request id = %q, want abc-123", got)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := httpapi.NewHandler(stubService{}, stubPinger{}, discard)

	if rec := do(t, h, http.MethodDelete, "/v1/events", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
