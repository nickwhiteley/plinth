package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/code"
)

// A Stream is an ordered set of migrations with its own version table (spec.md §4). plinth's is
// migrations.Plinth; a product's is its own. Streams are independent: plinth's runs first, and a
// product's migrations may reference plinth's tables but never alter them.
type Stream struct {
	Name string
	// FS holds the migrations, named NNNN_description.sql, at its root.
	FS fs.FS
	// Table is the version table, created in the current schema.
	Table string
}

// Migration is one file of a stream.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum [32]byte
}

var (
	// ErrMigrationName is a file that isn't NNNN_description.sql, a duplicate version, or a .down
	// file: migrations are forward-only, because a down that drops a log table destroys history.
	ErrMigrationName = code.New("db.migration_name")
	// ErrMigrationChanged is an applied migration whose file has since changed. Released
	// migrations are never edited; a change is a new migration.
	ErrMigrationChanged = code.New("db.migration_changed")
	// ErrMigrationUnknown is a version the database has applied that this code doesn't have: the
	// code is older than the database, and must not run against it.
	ErrMigrationUnknown = code.New("db.migration_unknown")
)

var (
	fileRe    = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)
	tableRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	advisoryK = int64(0x706c696e7468) // "plinth": one lock serialises every stream's migrations
)

// Migrations reads and orders a stream's files.
func (s Stream) Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(s.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, ErrMigrationName.With("stream", s.Name).With("file", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if seen[v] {
			return nil, ErrMigrationName.With("stream", s.Name).With("file", e.Name())
		}
		seen[v] = true
		b, err := fs.ReadFile(s.FS, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b), Checksum: sha256.Sum256(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies every stream's pending migrations, in order, and returns what it applied.
//
// conn must be the migration login. If owner is set, the session first does SET ROLE owner, so
// every object is owned by the owner role and not by the login (Furniture Magic data-model §2.1).
// The connection's search_path decides the schema; migrations never name one.
//
// Each migration runs in its own transaction, under one advisory lock, so two deploys can't
// migrate at once.
func Migrate(ctx context.Context, conn *pgx.Conn, owner string, streams ...Stream) ([]string, error) {
	if owner != "" {
		if _, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{owner}.Sanitize()); err != nil {
			return nil, err
		}
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryK); err != nil {
		return nil, err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryK) }()

	var applied []string
	for _, s := range streams {
		if !tableRe.MatchString(s.Table) {
			return applied, ErrMigrationName.With("stream", s.Name).With("table", s.Table)
		}
		files, err := s.Migrations()
		if err != nil {
			return applied, err
		}
		table := pgx.Identifier{s.Table}.Sanitize()
		if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			version    integer PRIMARY KEY,
			name       text NOT NULL,
			checksum   bytea NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now())`, table)); err != nil {
			return applied, err
		}
		done := map[int][]byte{}
		rows, err := conn.Query(ctx, "SELECT version, checksum FROM "+table)
		if err != nil {
			return applied, err
		}
		for rows.Next() {
			var v int
			var sum []byte
			if err := rows.Scan(&v, &sum); err != nil {
				rows.Close()
				return applied, err
			}
			done[v] = sum
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return applied, err
		}
		known := map[int]bool{}
		for _, m := range files {
			known[m.Version] = true
			if sum, ok := done[m.Version]; ok {
				if !bytes.Equal(sum, m.Checksum[:]) {
					return applied, ErrMigrationChanged.With("stream", s.Name).With("version", strconv.Itoa(m.Version))
				}
				continue
			}
			if err := apply(ctx, conn, table, m); err != nil {
				return applied, fmt.Errorf("%s %04d_%s: %w", s.Name, m.Version, m.Name, err)
			}
			applied = append(applied, fmt.Sprintf("%s %04d_%s", s.Name, m.Version, m.Name))
		}
		for v := range done {
			if !known[v] {
				return applied, ErrMigrationUnknown.With("stream", s.Name).With("version", strconv.Itoa(v))
			}
		}
	}
	return applied, nil
}

func apply(ctx context.Context, conn *pgx.Conn, table string, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// No arguments, so pgx sends it with the simple protocol, which allows many statements.
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (version, name, checksum) VALUES ($1, $2, $3)", m.Version, m.Name, m.Checksum[:]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
