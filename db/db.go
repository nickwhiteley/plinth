// Package db is plinth's Postgres layer (spec.md §3–§5): opening a pool, running work in a
// transaction that attributes it, the forward-only migration runner, and the table manifest that
// a product's grants read.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/actor"
)

// Open returns a pool for the runtime role, and fails now rather than on the first request if the
// database can't be reached: a database that can't be reached should stop a deploy.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parsing the connection string: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnIdleTime = 2 * time.Minute
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connecting: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: pinging: %w", err)
	}
	return pool, nil
}

// Beginner is what a store needs: a pool, or anything that can begin a transaction.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type txKey struct{}

// Run runs fn in a transaction. If ctx already carries one (an outer Run), fn joins it, so a
// product's write and a plinth write commit or roll back together. A new transaction first sets
// app.modified_by from actor.From(ctx), so the shadow log attributes every write in it.
//
// fn receives a context carrying the transaction; nested calls must use it.
func Run(ctx context.Context, b Beginner, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx, tx)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	by := ""
	if a, ok := actor.From(ctx); ok {
		by = a.String()
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.modified_by', $1, true)", by); err != nil {
		return err
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Querier runs statements: a pool, or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Q returns the transaction ctx carries, so a read inside Run sees its writes, or q otherwise.
func Q(ctx context.Context, q Querier) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return q
}

// Constraint returns the name of the constraint a write violated, or "" for any other error. The
// constraint names are part of the contract (Furniture Magic data-model §1.9), so stores map them
// to codes.
func Constraint(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.ConstraintName
	}
	return ""
}

// SQLState returns the SQLSTATE of a Postgres error, or "".
func SQLState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}
