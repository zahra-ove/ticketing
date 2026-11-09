package httpapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

type holdSeatsRequest struct {
	EventID uuid.UUID   `json:"event_id"`
	SeatIDs []uuid.UUID `json:"seat_ids"`
}

type createBookingRequest struct {
	HoldID uuid.UUID `json:"hold_id"`
}

type seatDTO struct {
	ID         uuid.UUID `json:"id"`
	Label      string    `json:"label"`
	PriceCents int64     `json:"price_cents"`
}

func toSeatDTOs(seats []domain.Seat) []seatDTO {
	out := make([]seatDTO, len(seats))
	for i, s := range seats {
		out[i] = seatDTO{ID: s.ID, Label: s.Label, PriceCents: s.PriceCents}
	}
	return out
}

type holdResponse struct {
	ID         uuid.UUID `json:"id"`
	EventID    uuid.UUID `json:"event_id"`
	Seats      []seatDTO `json:"seats"`
	TotalCents int64     `json:"total_cents"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func toHoldResponse(h *domain.Hold) holdResponse {
	return holdResponse{ID: h.ID, EventID: h.EventID, Seats: toSeatDTOs(h.Seats), TotalCents: h.TotalCents(), ExpiresAt: h.ExpiresAt}
}

type bookingResponse struct {
	ID         uuid.UUID `json:"id"`
	HoldID     uuid.UUID `json:"hold_id"`
	EventID    uuid.UUID `json:"event_id"`
	Status     string    `json:"status"`
	Seats      []seatDTO `json:"seats"`
	TotalCents int64     `json:"total_cents"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toBookingResponse(b *domain.Booking) bookingResponse {
	return bookingResponse{
		ID:         b.ID,
		HoldID:     b.HoldID,
		EventID:    b.EventID,
		Status:     string(b.Status),
		Seats:      toSeatDTOs(b.Seats),
		TotalCents: b.TotalCents,
		CreatedAt:  b.CreatedAt,
		UpdatedAt:  b.UpdatedAt,
	}
}

type availabilityResponse struct {
	EventID uuid.UUID   `json:"event_id"`
	Booked  []uuid.UUID `json:"booked_seat_ids"`
	Held    []uuid.UUID `json:"held_seat_ids"`
}

func toAvailabilityResponse(eventID uuid.UUID, av app.Availability) availabilityResponse {
	// Empty lists, not null, keep the contract simple for clients.
	resp := availabilityResponse{EventID: eventID, Booked: []uuid.UUID{}, Held: []uuid.UUID{}}
	resp.Booked = append(resp.Booked, av.Booked...)
	resp.Held = append(resp.Held, av.Held...)
	return resp
}
