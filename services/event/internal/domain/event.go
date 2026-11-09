// Package domain contains the event catalog model and its business rules.
// It has no dependencies on transport or storage code.
package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limits that keep a single event within a reasonable size.
const (
	MaxTitleLength       = 200
	MaxDescriptionLength = 5000
	MaxSeatsPerEvent     = 20000
	MaxSections          = 50
)

// Status is the lifecycle state of an event.
type Status string

// Event lifecycle: draft -> published -> cancelled, or draft -> cancelled.
const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusCancelled Status = "cancelled"
)

// Section is a block of seats that share the same price.
type Section struct {
	Name        string
	Rows        int
	SeatsPerRow int
	// PriceCents is the seat price in the smallest currency unit, to avoid floats.
	PriceCents int64
}

// Capacity is the number of seats in the section.
func (s Section) Capacity() int { return s.Rows * s.SeatsPerRow }

// Event is the aggregate root of the catalog.
type Event struct {
	ID          uuid.UUID
	Title       string
	Description string
	Venue       string
	StartsAt    time.Time
	Status      Status
	Sections    []Section
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewEventParams holds the input needed to create an event.
type NewEventParams struct {
	Title       string
	Description string
	Venue       string
	StartsAt    time.Time
	Sections    []Section
}

// NewEvent validates p and returns a new draft event.
func NewEvent(p NewEventParams, now time.Time) (*Event, error) {
	p.Title = strings.TrimSpace(p.Title)
	p.Venue = strings.TrimSpace(p.Venue)
	p.Description = strings.TrimSpace(p.Description)

	var v validator
	v.check(p.Title != "", "title", "must not be empty")
	v.check(utf8.RuneCountInString(p.Title) <= MaxTitleLength, "title", fmt.Sprintf("must be at most %d characters", MaxTitleLength))
	v.check(utf8.RuneCountInString(p.Description) <= MaxDescriptionLength, "description", fmt.Sprintf("must be at most %d characters", MaxDescriptionLength))
	v.check(p.Venue != "", "venue", "must not be empty")
	v.check(p.StartsAt.After(now), "starts_at", "must be in the future")
	validateSections(&v, p.Sections)

	if err := v.err(); err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate event id: %w", err)
	}

	now = now.UTC()
	return &Event{
		ID:          id,
		Title:       p.Title,
		Description: p.Description,
		Venue:       p.Venue,
		StartsAt:    p.StartsAt.UTC(),
		Status:      StatusDraft,
		Sections:    p.Sections,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func validateSections(v *validator, sections []Section) {
	v.check(len(sections) > 0, "sections", "at least one section is required")
	v.check(len(sections) <= MaxSections, "sections", fmt.Sprintf("at most %d sections are allowed", MaxSections))

	seen := make(map[string]bool, len(sections))
	total := 0
	for i, s := range sections {
		field := fmt.Sprintf("sections[%d]", i)
		name := strings.TrimSpace(s.Name)
		v.check(name != "", field+".name", "must not be empty")
		v.check(!seen[name], field+".name", "must be unique")
		v.check(s.Rows > 0, field+".rows", "must be positive")
		v.check(s.SeatsPerRow > 0, field+".seats_per_row", "must be positive")
		v.check(s.PriceCents >= 0, field+".price_cents", "must not be negative")
		seen[name] = true
		if s.Rows > 0 && s.SeatsPerRow > 0 {
			total += s.Capacity()
		}
	}
	v.check(total <= MaxSeatsPerEvent, "sections", fmt.Sprintf("an event can have at most %d seats", MaxSeatsPerEvent))
}

// Capacity is the total number of seats of the event.
func (e *Event) Capacity() int {
	total := 0
	for _, s := range e.Sections {
		total += s.Capacity()
	}
	return total
}

// Publish makes a draft event visible to customers.
func (e *Event) Publish(now time.Time) error {
	if e.Status != StatusDraft {
		return fmt.Errorf("%w: cannot publish a %s event", ErrInvalidTransition, e.Status)
	}
	if !e.StartsAt.After(now) {
		return fmt.Errorf("%w: cannot publish an event that has already started", ErrInvalidTransition)
	}
	e.Status = StatusPublished
	e.UpdatedAt = now.UTC()
	return nil
}

// Cancel cancels a draft or published event.
func (e *Event) Cancel(now time.Time) error {
	if e.Status == StatusCancelled {
		return fmt.Errorf("%w: event is already cancelled", ErrInvalidTransition)
	}
	e.Status = StatusCancelled
	e.UpdatedAt = now.UTC()
	return nil
}
