// Package postgres implements the booking repository on PostgreSQL with pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// Constraint names, matched on unique violations to tell conflicts apart.
const (
	uniqueHoldConstraint = "bookings_hold_id_key"
	uniqueSeatConstraint = "booking_seats_one_active_booking_per_seat"
	uniqueViolation      = "23505"
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

// Create implements app.Repository.
func (r *Repository) Create(ctx context.Context, b *domain.Booking) error {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO bookings (id, hold_id, event_id, customer_id, status, total_cents, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			b.ID, b.HoldID, b.EventID, b.CustomerID, string(b.Status), b.TotalCents, b.CreatedAt, b.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert booking: %w", err)
		}

		batch := &pgx.Batch{}
		for _, s := range b.Seats {
			batch.Queue(`
				INSERT INTO booking_seats (booking_id, event_id, seat_id, label, price_cents, active)
				VALUES ($1, $2, $3, $4, $5, true)`,
				b.ID, b.EventID, s.ID, s.Label, s.PriceCents)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert booking seats: %w", err)
		}
		return nil
	})

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case uniqueHoldConstraint:
			return app.ErrHoldAlreadyUsed
		case uniqueSeatConstraint:
			// Find out which seats are taken so the client can pick others.
			taken, lookupErr := r.BookedSeats(context.WithoutCancel(ctx), b.EventID, seatIDs(b.Seats))
			if lookupErr != nil {
				return fmt.Errorf("seat already booked; list taken seats: %w", lookupErr)
			}
			return &domain.SeatsUnavailableError{SeatIDs: taken}
		}
	}
	return err
}

const selectBooking = `
SELECT id, hold_id, event_id, customer_id, status, total_cents, created_at, updated_at
FROM bookings`

// Get implements app.Repository.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*domain.Booking, error) {
	return r.getOne(ctx, r.pool, selectBooking+` WHERE id = $1`, id)
}

// GetByHoldID implements app.Repository.
func (r *Repository) GetByHoldID(ctx context.Context, holdID uuid.UUID) (*domain.Booking, error) {
	return r.getOne(ctx, r.pool, selectBooking+` WHERE hold_id = $1`, holdID)
}

// Update implements app.Repository.
func (r *Repository) Update(ctx context.Context, id uuid.UUID, fn func(*domain.Booking) error) (*domain.Booking, error) {
	var updated *domain.Booking
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		b, err := r.getOne(ctx, tx, selectBooking+` WHERE id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `UPDATE bookings SET status = $2, updated_at = $3 WHERE id = $1`,
			b.ID, string(b.Status), b.UpdatedAt); err != nil {
			return fmt.Errorf("update booking: %w", err)
		}
		// Cancelled bookings drop out of the unique index, freeing their seats.
		if _, err := tx.Exec(ctx, `UPDATE booking_seats SET active = $2 WHERE booking_id = $1`,
			b.ID, b.Active()); err != nil {
			return fmt.Errorf("update booking seats: %w", err)
		}
		updated = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// BookedSeats implements app.Repository.
func (r *Repository) BookedSeats(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if len(ids) == 0 {
		rows, err = r.pool.Query(ctx, `SELECT seat_id FROM booking_seats WHERE event_id = $1 AND active`, eventID)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT seat_id FROM booking_seats WHERE event_id = $1 AND active AND seat_id = ANY($2)`, eventID, ids)
	}
	if err != nil {
		return nil, fmt.Errorf("query booked seats: %w", err)
	}
	seats, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("scan booked seats: %w", err)
	}
	return seats, nil
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *Repository) getOne(ctx context.Context, q querier, sql string, arg any) (*domain.Booking, error) {
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("query booking: %w", err)
	}
	b, err := pgx.CollectOneRow(rows, func(row pgx.CollectableRow) (domain.Booking, error) {
		var (
			b      domain.Booking
			status string
		)
		err := row.Scan(&b.ID, &b.HoldID, &b.EventID, &b.CustomerID, &status, &b.TotalCents, &b.CreatedAt, &b.UpdatedAt)
		b.Status = domain.Status(status)
		b.CreatedAt, b.UpdatedAt = b.CreatedAt.UTC(), b.UpdatedAt.UTC()
		return b, err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrBookingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan booking: %w", err)
	}

	seatRows, err := q.Query(ctx, `
		SELECT seat_id, label, price_cents FROM booking_seats WHERE booking_id = $1 ORDER BY label`, b.ID)
	if err != nil {
		return nil, fmt.Errorf("query booking seats: %w", err)
	}
	b.Seats, err = pgx.CollectRows(seatRows, func(row pgx.CollectableRow) (domain.Seat, error) {
		var s domain.Seat
		err := row.Scan(&s.ID, &s.Label, &s.PriceCents)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan booking seats: %w", err)
	}
	return &b, nil
}

func seatIDs(seats []domain.Seat) []uuid.UUID {
	ids := make([]uuid.UUID, len(seats))
	for i, s := range seats {
		ids[i] = s.ID
	}
	return ids
}
