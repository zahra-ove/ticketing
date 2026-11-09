package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// Seat is a single bookable seat of an event. Seats are part of the catalog;
// whether a seat is held or sold is owned by the booking service.
type Seat struct {
	ID         uuid.UUID
	EventID    uuid.UUID
	Section    string
	Row        int
	Number     int
	PriceCents int64
}

// Label is the human-readable seat position, e.g. "VIP-3-12".
func (s Seat) Label() string {
	return fmt.Sprintf("%s-%d-%d", s.Section, s.Row, s.Number)
}

// GenerateSeats builds the seat map from the event sections.
// Rows and seat numbers start at 1.
func (e *Event) GenerateSeats() ([]Seat, error) {
	seats := make([]Seat, 0, e.Capacity())
	for _, sec := range e.Sections {
		for row := 1; row <= sec.Rows; row++ {
			for num := 1; num <= sec.SeatsPerRow; num++ {
				id, err := uuid.NewV7()
				if err != nil {
					return nil, fmt.Errorf("generate seat id: %w", err)
				}
				seats = append(seats, Seat{
					ID:         id,
					EventID:    e.ID,
					Section:    sec.Name,
					Row:        row,
					Number:     num,
					PriceCents: sec.PriceCents,
				})
			}
		}
	}
	return seats, nil
}
