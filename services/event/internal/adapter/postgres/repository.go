// Package postgres implements the event repository on top of PostgreSQL using pgx.
//
// Queries are written by hand: there are only a handful of them, and plain SQL
// keeps the data access explicit and easy to review.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// Repository implements app.Repository.
type Repository struct {
	pool *pgxpool.Pool
}

var _ app.Repository = (*Repository)(nil)

// NewRepository returns a Repository that uses pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectEvent = `
SELECT id, title, description, venue, starts_at, status, sections, created_at, updated_at
FROM events`

// sectionRow is the JSON shape of a section in the events.sections column.
// It is separate from domain.Section so the storage format can evolve on its own.
type sectionRow struct {
	Name        string `json:"name"`
	Rows        int    `json:"rows"`
	SeatsPerRow int    `json:"seats_per_row"`
	PriceCents  int64  `json:"price_cents"`
}

// Create implements app.Repository.
func (r *Repository) Create(ctx context.Context, ev *domain.Event, seats []domain.Seat) error {
	sections, err := encodeSections(ev.Sections)
	if err != nil {
		return err
	}

	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO events (id, title, description, venue, starts_at, status, sections, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			ev.ID, ev.Title, ev.Description, ev.Venue, ev.StartsAt, string(ev.Status), sections, ev.CreatedAt, ev.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		sectionIndex := make(map[string]int, len(ev.Sections))
		for i, s := range ev.Sections {
			sectionIndex[s.Name] = i
		}

		// COPY is much faster than individual INSERTs for thousands of seats.
		_, err = tx.CopyFrom(ctx,
			pgx.Identifier{"seats"},
			[]string{"id", "event_id", "section", "section_index", "row_number", "seat_number", "price_cents"},
			pgx.CopyFromSlice(len(seats), func(i int) ([]any, error) {
				s := seats[i]
				return []any{s.ID, s.EventID, s.Section, sectionIndex[s.Section], s.Row, s.Number, s.PriceCents}, nil
			}),
		)
		if err != nil {
			return fmt.Errorf("copy seats: %w", err)
		}
		return nil
	})
}

// Get implements app.Repository.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	rows, err := r.pool.Query(ctx, selectEvent+` WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("query event: %w", err)
	}
	ev, err := pgx.CollectOneRow(rows, scanEvent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return &ev, nil
}

// ListPublished implements app.Repository.
func (r *Repository) ListPublished(ctx context.Context, after *app.Cursor, limit int) ([]domain.Event, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = r.pool.Query(ctx, selectEvent+`
			WHERE status = 'published'
			ORDER BY starts_at, id
			LIMIT $1`, limit)
	} else {
		// Row comparison matches the (starts_at, id) index exactly.
		rows, err = r.pool.Query(ctx, selectEvent+`
			WHERE status = 'published' AND (starts_at, id) > ($1, $2)
			ORDER BY starts_at, id
			LIMIT $3`, after.StartsAt, after.ID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query published events: %w", err)
	}

	events, err := pgx.CollectRows(rows, scanEvent)
	if err != nil {
		return nil, fmt.Errorf("scan published events: %w", err)
	}
	return events, nil
}

// Update implements app.Repository. SELECT ... FOR UPDATE locks the row, so
// two concurrent updates of the same event run one after the other.
func (r *Repository) Update(ctx context.Context, id uuid.UUID, fn func(*domain.Event) error) (*domain.Event, error) {
	var updated domain.Event

	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectEvent+` WHERE id = $1 FOR UPDATE`, id)
		if err != nil {
			return fmt.Errorf("query event: %w", err)
		}
		ev, err := pgx.CollectOneRow(rows, scanEvent)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("scan event: %w", err)
		}

		if err := fn(&ev); err != nil {
			return err
		}

		sections, err := encodeSections(ev.Sections)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE events
			SET title = $2, description = $3, venue = $4, starts_at = $5, status = $6, sections = $7, updated_at = $8
			WHERE id = $1`,
			ev.ID, ev.Title, ev.Description, ev.Venue, ev.StartsAt, string(ev.Status), sections, ev.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("update event: %w", err)
		}

		updated = ev
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// ListSeats implements app.Repository.
func (r *Repository) ListSeats(ctx context.Context, eventID uuid.UUID) ([]domain.Seat, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, event_id, section, row_number, seat_number, price_cents
		FROM seats
		WHERE event_id = $1
		ORDER BY section_index, row_number, seat_number`, eventID)
	if err != nil {
		return nil, fmt.Errorf("query seats: %w", err)
	}

	seats, err := pgx.CollectRows(rows, scanSeat)
	if err != nil {
		return nil, fmt.Errorf("scan seats: %w", err)
	}
	return seats, nil
}

// GetSeats implements app.Repository.
func (r *Repository) GetSeats(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) ([]domain.Seat, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, event_id, section, row_number, seat_number, price_cents
		FROM seats
		WHERE event_id = $1 AND id = ANY($2)
		ORDER BY section_index, row_number, seat_number`, eventID, ids)
	if err != nil {
		return nil, fmt.Errorf("query seats: %w", err)
	}

	seats, err := pgx.CollectRows(rows, scanSeat)
	if err != nil {
		return nil, fmt.Errorf("scan seats: %w", err)
	}
	return seats, nil
}

func scanSeat(row pgx.CollectableRow) (domain.Seat, error) {
	var s domain.Seat
	err := row.Scan(&s.ID, &s.EventID, &s.Section, &s.Row, &s.Number, &s.PriceCents)
	return s, err
}

func scanEvent(row pgx.CollectableRow) (domain.Event, error) {
	var (
		ev       domain.Event
		status   string
		sections []byte
	)
	err := row.Scan(&ev.ID, &ev.Title, &ev.Description, &ev.Venue, &ev.StartsAt, &status, &sections, &ev.CreatedAt, &ev.UpdatedAt)
	if err != nil {
		return domain.Event{}, err
	}
	ev.Status = domain.Status(status)
	ev.StartsAt = ev.StartsAt.UTC()
	ev.CreatedAt = ev.CreatedAt.UTC()
	ev.UpdatedAt = ev.UpdatedAt.UTC()

	if ev.Sections, err = decodeSections(sections); err != nil {
		return domain.Event{}, err
	}
	return ev, nil
}

func encodeSections(sections []domain.Section) ([]byte, error) {
	out := make([]sectionRow, len(sections))
	for i, s := range sections {
		out[i] = sectionRow{Name: s.Name, Rows: s.Rows, SeatsPerRow: s.SeatsPerRow, PriceCents: s.PriceCents}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode sections: %w", err)
	}
	return b, nil
}

func decodeSections(b []byte) ([]domain.Section, error) {
	var in []sectionRow
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("decode sections: %w", err)
	}
	out := make([]domain.Section, len(in))
	for i, s := range in {
		out[i] = domain.Section{Name: s.Name, Rows: s.Rows, SeatsPerRow: s.SeatsPerRow, PriceCents: s.PriceCents}
	}
	return out, nil
}
