// Package postgres implements the payment repository on PostgreSQL with pgx.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	paymentv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/payment/v1"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/pkg/outbox"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/services/payment/internal/app"
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending migrations of the payment service.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return postgres.Migrate(ctx, pool, migrationsFS, "migrations")
}

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
func (r *Repository) Create(ctx context.Context, p *domain.Payment) error {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO payments (id, booking_id, customer_id, amount_cents, status, failure_reason, provider_reference, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9)`,
			p.ID, p.BookingID, p.CustomerID, p.AmountCents, string(p.Status), p.FailureReason, p.ProviderRef, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}
		return saveEvents(ctx, tx, p.PullEvents())
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return app.ErrDuplicatePayment
	}
	return err
}

const selectPayment = `
SELECT id, booking_id, customer_id, amount_cents, status,
       coalesce(failure_reason, ''), coalesce(provider_reference, ''), created_at, updated_at
FROM payments`

// GetByBookingID implements app.Repository.
func (r *Repository) GetByBookingID(ctx context.Context, bookingID uuid.UUID) (*domain.Payment, error) {
	return getOne(ctx, r.pool, selectPayment+` WHERE booking_id = $1`, bookingID)
}

// Update implements app.Repository.
func (r *Repository) Update(ctx context.Context, id uuid.UUID, fn func(*domain.Payment) error) (*domain.Payment, error) {
	var updated *domain.Payment
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		p, err := getOne(ctx, tx, selectPayment+` WHERE id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payments SET status = $2, updated_at = $3 WHERE id = $1`,
			p.ID, string(p.Status), p.UpdatedAt); err != nil {
			return fmt.Errorf("update payment: %w", err)
		}
		if err := saveEvents(ctx, tx, p.PullEvents()); err != nil {
			return err
		}
		updated = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getOne(ctx context.Context, q querier, sql string, arg any) (*domain.Payment, error) {
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("query payment: %w", err)
	}
	p, err := pgx.CollectOneRow(rows, func(row pgx.CollectableRow) (domain.Payment, error) {
		var (
			p      domain.Payment
			status string
		)
		err := row.Scan(&p.ID, &p.BookingID, &p.CustomerID, &p.AmountCents, &status, &p.FailureReason, &p.ProviderRef, &p.CreatedAt, &p.UpdatedAt)
		p.Status = domain.Status(status)
		p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
		return p, err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan payment: %w", err)
	}
	return &p, nil
}

const source = "payment"

// saveEvents maps domain events to the public contract and stores them in the outbox.
func saveEvents(ctx context.Context, tx pgx.Tx, events []domain.Event) error {
	for _, e := range events {
		var (
			payload proto.Message
			p       domain.Payment
		)
		switch e := e.(type) {
		case domain.PaymentSucceeded:
			p = e.Payment
			payload = &paymentv1.PaymentSucceeded{
				PaymentId: p.ID.String(), BookingId: p.BookingID.String(), CustomerId: p.CustomerID.String(),
				AmountCents: p.AmountCents, ProviderReference: p.ProviderRef, SucceededAt: timestamppb.New(p.UpdatedAt),
			}
		case domain.PaymentFailed:
			p = e.Payment
			payload = &paymentv1.PaymentFailed{
				PaymentId: p.ID.String(), BookingId: p.BookingID.String(), CustomerId: p.CustomerID.String(),
				AmountCents: p.AmountCents, Reason: p.FailureReason, FailedAt: timestamppb.New(p.UpdatedAt),
			}
		case domain.PaymentRefunded:
			p = e.Payment
			payload = &paymentv1.PaymentRefunded{
				PaymentId: p.ID.String(), BookingId: p.BookingID.String(), CustomerId: p.CustomerID.String(),
				AmountCents: p.AmountCents, RefundedAt: timestamppb.New(p.UpdatedAt),
			}
		default:
			return fmt.Errorf("no contract for domain event %T", e)
		}

		// Keyed by booking id, like the booking events, so one booking's saga stays ordered.
		msg, err := messaging.New(messaging.TopicPaymentEvents, p.BookingID.String(), source, payload, p.UpdatedAt)
		if err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, msg); err != nil {
			return err
		}
	}
	return nil
}
