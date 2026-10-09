package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/code"
)

var (
	// ErrMigrationPending is a migration the code embeds that the database hasn't recorded: the
	// deploy didn't run the migrator, or it failed.
	ErrMigrationPending = code.New("db.migration_pending")
	// ErrMigrationTableMissing is a stream whose version table doesn't exist: nothing of it has
	// ever been migrated. It is not reported as ErrMigrationPending.
	ErrMigrationTableMissing = code.New("db.migration_table_missing")
)

// StreamStatus is what Pending found for one stream. Every entry is "<stream> NNNN_name".
type StreamStatus struct {
	Stream string
	// Missing is true when the stream's version table doesn't exist. Pending then lists every
	// migration the code embeds.
	Missing bool
	// Pending are embedded migrations the database hasn't recorded, in order.
	Pending []string
	// Unknown are recorded versions the code doesn't have (the code is older than the database),
	// named as the database recorded them.
	Unknown []string
	// Changed are recorded versions whose checksum differs from the code's file.
	Changed []string
}

// OK reports whether the stream is exactly as the code expects.
func (s StreamStatus) OK() bool {
	return !s.Missing && len(s.Pending) == 0 && len(s.Unknown) == 0 && len(s.Changed) == 0
}

// Status is what Pending found, one entry per stream in the order given.
type Status struct {
	Streams []StreamStatus
}

// OK reports whether every stream is fully migrated, with nothing unknown or changed.
func (s Status) OK() bool {
	for _, st := range s.Streams {
		if !st.OK() {
			return false
		}
	}
	return true
}

// Pending lists the migrations pending across all streams, in order.
func (s Status) Pending() []string {
	var out []string
	for _, st := range s.Streams {
		out = append(out, st.Pending...)
	}
	return out
}

// Err is nil when OK, and otherwise every problem joined, so errors.Is finds each kind:
// ErrMigrationChanged and ErrMigrationUnknown (as Migrate returns them), ErrMigrationTableMissing
// and ErrMigrationPending. Each carries the stream and, for the first three, the version or the
// migration's name.
func (s Status) Err() error {
	var errs []error
	for _, st := range s.Streams {
		for _, m := range st.Changed {
			errs = append(errs, ErrMigrationChanged.With("stream", st.Stream).With("migration", m))
		}
		for _, m := range st.Unknown {
			errs = append(errs, ErrMigrationUnknown.With("stream", st.Stream).With("migration", m))
		}
		if st.Missing {
			errs = append(errs, ErrMigrationTableMissing.With("stream", st.Stream))
		} else if len(st.Pending) > 0 {
			errs = append(errs, ErrMigrationPending.With("stream", st.Stream).With("count", strconv.Itoa(len(st.Pending))).With("next", st.Pending[0]))
		}
	}
	return errors.Join(errs...)
}

// Pending reports, without applying anything, whether every migration the code embeds has been
// applied: the check a product's server makes at boot, as its runtime role (spec.md §4).
//
// It only reads. It takes no lock, doesn't touch the search_path (the version tables are found on
// q's, as Migrate finds them), and needs only SELECT on the version tables, which Grant gives the
// runtime role. A failure to read (no privilege, no connection) is an error, not a Status.
func Pending(ctx context.Context, q Querier, streams ...Stream) (Status, error) {
	var out Status
	for _, s := range streams {
		if !tableRe.MatchString(s.Table) {
			return out, ErrMigrationName.With("stream", s.Name).With("table", s.Table)
		}
		files, err := s.Migrations()
		if err != nil {
			return out, err
		}
		st := StreamStatus{Stream: s.Name}
		table := pgx.Identifier{s.Table}.Sanitize()
		var exists bool
		if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return out, fmt.Errorf("db: %s: %w", s.Name, err)
		}
		type row struct {
			name string
			sum  []byte
		}
		done := map[int]row{}
		if exists {
			rows, err := q.Query(ctx, "SELECT version, name, checksum FROM "+table)
			if err != nil {
				return out, fmt.Errorf("db: %s: %w", s.Name, err)
			}
			for rows.Next() {
				var v int
				var r row
				if err := rows.Scan(&v, &r.name, &r.sum); err != nil {
					rows.Close()
					return out, fmt.Errorf("db: %s: %w", s.Name, err)
				}
				done[v] = r
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return out, fmt.Errorf("db: %s: %w", s.Name, err)
			}
		}
		st.Missing = !exists
		known := map[int]bool{}
		for _, m := range files {
			known[m.Version] = true
			name := fmt.Sprintf("%s %04d_%s", s.Name, m.Version, m.Name)
			r, ok := done[m.Version]
			switch {
			case !ok:
				st.Pending = append(st.Pending, name)
			case !bytes.Equal(r.sum, m.Checksum[:]):
				st.Changed = append(st.Changed, name)
			}
		}
		for v, r := range done {
			if !known[v] {
				st.Unknown = append(st.Unknown, fmt.Sprintf("%s %04d_%s", s.Name, v, r.name))
			}
		}
		slices.Sort(st.Unknown) // "NNNN_name" sorts by version
		out.Streams = append(out.Streams, st)
	}
	return out, nil
}
