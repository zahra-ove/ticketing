// Package postgres implements the inbox on PostgreSQL.
package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/services/notification/internal/app"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending migrations of the notification service.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return postgres.Migrate(ctx, pool, migrationsFS, "migrations")
}

// Inbox implements app.Inbox.
type Inbox struct {
	pool *pgxpool.Pool
}

var _ app.Inbox = (*Inbox)(nil)

// NewInbox returns an Inbox backed by pool.
func NewInbox(pool *pgxpool.Pool) *Inbox {
	return &Inbox{pool: pool}
}

// Once implements app.Inbox. The message id is inserted in a transaction that
// stays open while fn runs: the row lock makes a concurrent duplicate wait,
// and a failing fn rolls the insert back so the message is retried.
//
// One gap remains, by nature of email: if fn succeeds and the commit then
// fails, the message is retried and the email is sent twice. That is far
// rarer than redelivery and the accepted trade-off for at-least-once sends.
func (i *Inbox) Once(ctx context.Context, messageID uuid.UUID, fn func(ctx context.Context) error) error {
	return pgx.BeginFunc(ctx, i.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO inbox (message_id) VALUES ($1) ON CONFLICT (message_id) DO NOTHING`, messageID)
		if err != nil {
			return fmt.Errorf("record message %s: %w", messageID, err)
		}
		if tag.RowsAffected() == 0 {
			return nil // already processed
		}
		return fn(ctx)
	})
}

// DeleteOlderThan removes inbox rows older than the given number of days.
// Messages are not redelivered after that, so their ids are no longer needed.
func (i *Inbox) DeleteOlderThan(ctx context.Context, days int) (int64, error) {
	tag, err := i.pool.Exec(ctx, `DELETE FROM inbox WHERE processed_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, fmt.Errorf("clean inbox: %w", err)
	}
	return tag.RowsAffected(), nil
}
