package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func validParams() domain.NewEventParams {
	return domain.NewEventParams{
		Title:    "Go Conference",
		Venue:    "Tehran Hall",
		StartsAt: now.Add(30 * 24 * time.Hour),
		Sections: []domain.Section{
			{Name: "VIP", Rows: 2, SeatsPerRow: 5, PriceCents: 50_00},
			{Name: "Standard", Rows: 10, SeatsPerRow: 20, PriceCents: 20_00},
		},
	}
}

func TestNewEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(p *domain.NewEventParams)
		wantFields []string
	}{
		{name: "valid", mutate: func(*domain.NewEventParams) {}},
		{name: "empty title", mutate: func(p *domain.NewEventParams) { p.Title = "   " }, wantFields: []string{"title"}},
		{name: "title too long", mutate: func(p *domain.NewEventParams) { p.Title = strings.Repeat("a", domain.MaxTitleLength+1) }, wantFields: []string{"title"}},
		{name: "empty venue", mutate: func(p *domain.NewEventParams) { p.Venue = "" }, wantFields: []string{"venue"}},
		{name: "starts in the past", mutate: func(p *domain.NewEventParams) { p.StartsAt = now.Add(-time.Hour) }, wantFields: []string{"starts_at"}},
		{name: "no sections", mutate: func(p *domain.NewEventParams) { p.Sections = nil }, wantFields: []string{"sections"}},
		{
			name: "invalid section",
			mutate: func(p *domain.NewEventParams) {
				p.Sections = []domain.Section{{Name: "", Rows: 0, SeatsPerRow: -1, PriceCents: -5}}
			},
			wantFields: []string{"sections[0].name", "sections[0].rows", "sections[0].seats_per_row", "sections[0].price_cents"},
		},
		{
			name: "duplicate section names",
			mutate: func(p *domain.NewEventParams) {
				p.Sections = []domain.Section{{Name: "A", Rows: 1, SeatsPerRow: 1}, {Name: "A", Rows: 1, SeatsPerRow: 1}}
			},
			wantFields: []string{"sections[1].name"},
		},
		{
			name: "too many seats",
			mutate: func(p *domain.NewEventParams) {
				p.Sections = []domain.Section{{Name: "A", Rows: 1000, SeatsPerRow: 1000}}
			},
			wantFields: []string{"sections"},
		},
		{
			name:       "reports every invalid field",
			mutate:     func(p *domain.NewEventParams) { p.Title = ""; p.Venue = "" },
			wantFields: []string{"title", "venue"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := validParams()
			tt.mutate(&p)

			ev, err := domain.NewEvent(p, now)

			if len(tt.wantFields) == 0 {
				if err != nil {
					t.Fatalf("NewEvent() unexpected error: %v", err)
				}
				if ev.Status != domain.StatusDraft {
					t.Errorf("status = %q, want %q", ev.Status, domain.StatusDraft)
				}
				return
			}

			var verr *domain.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("NewEvent() error = %v, want *ValidationError", err)
			}
			if len(verr.Fields) != len(tt.wantFields) {
				t.Errorf("got fields %v, want %v", verr.Fields, tt.wantFields)
			}
			for _, f := range tt.wantFields {
				if _, ok := verr.Fields[f]; !ok {
					t.Errorf("missing validation error for field %q (got %v)", f, verr.Fields)
				}
			}
		})
	}
}

func TestEvent_Publish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  domain.Status
		at      time.Time
		wantErr error
	}{
		{name: "draft can be published", status: domain.StatusDraft, at: now},
		{name: "published cannot be published again", status: domain.StatusPublished, at: now, wantErr: domain.ErrInvalidTransition},
		{name: "cancelled cannot be published", status: domain.StatusCancelled, at: now, wantErr: domain.ErrInvalidTransition},
		{name: "started event cannot be published", status: domain.StatusDraft, at: now.Add(365 * 24 * time.Hour), wantErr: domain.ErrInvalidTransition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ev, err := domain.NewEvent(validParams(), now)
			if err != nil {
				t.Fatal(err)
			}
			ev.Status = tt.status

			err = ev.Publish(tt.at)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Publish() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && ev.Status != domain.StatusPublished {
				t.Errorf("status = %q, want published", ev.Status)
			}
		})
	}
}

func TestEvent_Cancel(t *testing.T) {
	t.Parallel()

	ev, err := domain.NewEvent(validParams(), now)
	if err != nil {
		t.Fatal(err)
	}

	if err := ev.Cancel(now); err != nil {
		t.Fatalf("first Cancel() error = %v", err)
	}
	if err := ev.Cancel(now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second Cancel() error = %v, want ErrInvalidTransition", err)
	}
}

func TestEvent_GenerateSeats(t *testing.T) {
	t.Parallel()

	ev, err := domain.NewEvent(validParams(), now)
	if err != nil {
		t.Fatal(err)
	}

	seats, err := ev.GenerateSeats()
	if err != nil {
		t.Fatal(err)
	}

	if len(seats) != ev.Capacity() {
		t.Fatalf("got %d seats, want %d", len(seats), ev.Capacity())
	}
	if got, want := seats[0].Label(), "VIP-1-1"; got != want {
		t.Errorf("first seat = %q, want %q", got, want)
	}
	if got, want := seats[len(seats)-1].Label(), "Standard-10-20"; got != want {
		t.Errorf("last seat = %q, want %q", got, want)
	}

	ids := make(map[string]bool, len(seats))
	for _, s := range seats {
		if ids[s.ID.String()] {
			t.Fatalf("duplicate seat id %s", s.ID)
		}
		ids[s.ID.String()] = true
	}
}
