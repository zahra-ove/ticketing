package postgres

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zahra-ove/ticketing/pkg/postgres"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending migrations of the booking service.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return postgres.Migrate(ctx, pool, migrationsFS, "migrations")
}
