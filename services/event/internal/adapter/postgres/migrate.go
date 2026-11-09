package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending migrations. The migrations are embedded in the
// binary, so the image needs no extra files. A Postgres advisory lock makes it
// safe to run from several replicas at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied int, err error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return 0, fmt.Errorf("open migrations: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return 0, fmt.Errorf("create migration locker: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close migration connection: %w", cerr)
		}
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return 0, fmt.Errorf("create migration provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	return len(results), nil
}
