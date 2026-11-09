# 3. Hand-written SQL with pgx, migrations with goose

Date: 2026-11-09

## Status

Accepted

## Context

Services need a Postgres access layer and schema migrations. Options were an
ORM (GORM, ent), a code generator (sqlc), or hand-written SQL on pgx.

## Decision

- Use `pgx/v5` directly with hand-written SQL inside each service's
  `internal/adapter/postgres` package. Rows are mapped explicitly to domain types.
- Use `goose` for migrations. SQL files are embedded with `go:embed`, so the
  service binary runs its own migrations (`<service> migrate`) and the image
  needs no extra files. A Postgres advisory lock prevents concurrent runs.
- In Docker Compose, migrations run as a one-shot container; the service starts
  only after it exits successfully.

## Consequences

- No reflection and no hidden queries: every SQL statement is visible in review
  and can use Postgres features (row comparison, `FOR UPDATE`, `COPY`) directly.
- Mapping code is written by hand. With a few queries per service this is cheap;
  if a service grows many queries, sqlc is the natural next step because it keeps
  plain SQL and generates the mapping.
- ORMs were rejected because they hide query shape and locking behaviour, which
  are central to this project (seat reservation under concurrency).
