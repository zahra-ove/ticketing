package httpapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// The API has its own request and response types. Keeping them separate from
// the domain lets the API contract and the domain model change independently.

type sectionDTO struct {
	Name        string `json:"name"`
	Rows        int    `json:"rows"`
	SeatsPerRow int    `json:"seats_per_row"`
	PriceCents  int64  `json:"price_cents"`
}

type createEventRequest struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Venue       string       `json:"venue"`
	StartsAt    time.Time    `json:"starts_at"`
	Sections    []sectionDTO `json:"sections"`
}

func (r createEventRequest) toParams() domain.NewEventParams {
	sections := make([]domain.Section, len(r.Sections))
	for i, s := range r.Sections {
		sections[i] = domain.Section{Name: s.Name, Rows: s.Rows, SeatsPerRow: s.SeatsPerRow, PriceCents: s.PriceCents}
	}
	return domain.NewEventParams{
		Title:       r.Title,
		Description: r.Description,
		Venue:       r.Venue,
		StartsAt:    r.StartsAt,
		Sections:    sections,
	}
}

type eventResponse struct {
	ID          uuid.UUID    `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Venue       string       `json:"venue"`
	StartsAt    time.Time    `json:"starts_at"`
	Status      string       `json:"status"`
	Capacity    int          `json:"capacity"`
	Sections    []sectionDTO `json:"sections"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

func toEventResponse(ev *domain.Event) eventResponse {
	sections := make([]sectionDTO, len(ev.Sections))
	for i, s := range ev.Sections {
		sections[i] = sectionDTO{Name: s.Name, Rows: s.Rows, SeatsPerRow: s.SeatsPerRow, PriceCents: s.PriceCents}
	}
	return eventResponse{
		ID:          ev.ID,
		Title:       ev.Title,
		Description: ev.Description,
		Venue:       ev.Venue,
		StartsAt:    ev.StartsAt,
		Status:      string(ev.Status),
		Capacity:    ev.Capacity(),
		Sections:    sections,
		CreatedAt:   ev.CreatedAt,
		UpdatedAt:   ev.UpdatedAt,
	}
}

type listEventsResponse struct {
	Data       []eventResponse `json:"data"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type seatResponse struct {
	ID         uuid.UUID `json:"id"`
	Label      string    `json:"label"`
	Section    string    `json:"section"`
	Row        int       `json:"row"`
	Number     int       `json:"number"`
	PriceCents int64     `json:"price_cents"`
}

type listSeatsResponse struct {
	Data []seatResponse `json:"data"`
}

func toSeatResponses(seats []domain.Seat) []seatResponse {
	out := make([]seatResponse, len(seats))
	for i, s := range seats {
		out[i] = seatResponse{
			ID:         s.ID,
			Label:      s.Label(),
			Section:    s.Section,
			Row:        s.Row,
			Number:     s.Number,
			PriceCents: s.PriceCents,
		}
	}
	return out
}
