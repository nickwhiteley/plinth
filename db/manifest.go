package db

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Class is how a table is treated (Furniture Magic data-model §1.6, §2.3).
type Class string

const (
	// Entity tables are soft-deleted: the runtime role has no DELETE on them.
	Entity Class = "entity"
	// Reference tables are reconciled from code or configured by administrators, never deleted.
	Reference Class = "reference"
	// AppendOnly tables only ever gain rows (a save, an identity ever issued): logged, never
	// deleted or rewritten by the application.
	AppendOnly Class = "append_only"
	// Counter tables are hot rows updated in place (a head's revision), whose history is kept
	// elsewhere: not logged, and never deleted.
	Counter Class = "counter"
	// Link tables are hard-deleted; the log's D row (the old row) is the history.
	Link Class = "link"
	// Ephemeral tables (sessions, tokens) are hard-deleted and not logged.
	Ephemeral Class = "ephemeral"
	// Record tables (the communication log) keep their own history with their own retention:
	// hard-deleted when they expire, and not shadow-logged, because the log would copy the
	// personal data they hold and erasure would have to scrub it twice.
	Record Class = "record"
	// Journal tables are a history the product writes itself (a command log): appended to and
	// never rewritten or deleted, like append-only ones, but not shadow-logged, because they are
	// already the record. The warehouse reads them through an extract view.
	Journal Class = "journal"
	// Internal tables (a migration version table) are no role's business but the owner's.
	Internal Class = "internal"
)

// Table is one table's entry in the manifest (spec.md §4).
type Table struct {
	Name  string
	Class Class
	// Secrets are columns no read-only or extract role may read, and the log never keeps.
	Secrets []string
	// NoExtract withholds the table's log twin from the extract role: a log the warehouse has no
	// business with, such as encrypted settings, whose ciphertext the log must keep for history.
	NoExtract bool
}

// Deletable reports whether the runtime role may delete from the table.
func (t Table) Deletable() bool { return t.Class == Link || t.Class == Ephemeral || t.Class == Record }

// Logged reports whether the table has a shadow log twin.
func (t Table) Logged() bool {
	return t.Class != Ephemeral && t.Class != Internal && t.Class != Record && t.Class != Counter && t.Class != Journal
}

// Roles are the product's database roles. plinth never names them.
type Roles struct {
	// App is the runtime role.
	App string
	// Extract reads only the log schema and the extract views.
	Extract string
	// Readonly is break-glass support: a column allowlist without secrets.
	Readonly string
}

// Grant gives each role exactly what the manifest says, in the current schema and its log schema.
// Run it as the owner on every deploy: it isn't a migration, because which roles exist is
// deployment configuration, and re-running is what keeps "every table" true.
//
// The manifest is every table the deployment has: plinth's and the product's.
func Grant(ctx context.Context, tx pgx.Tx, roles Roles, tables []Table) error {
	var schema string
	if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	app, logs := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{schema + "_log"}.Sanitize()
	id := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec := func(q string) error {
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("db: %s: %w", q, err)
		}
		return nil
	}
	if roles.App != "" {
		if err := exec("GRANT USAGE ON SCHEMA " + app + ", " + logs + " TO " + id(roles.App)); err != nil {
			return err
		}
		if err := exec("GRANT SELECT ON ALL TABLES IN SCHEMA " + logs + " TO " + id(roles.App)); err != nil {
			return err
		}
	}
	if roles.Extract != "" {
		if err := exec("GRANT USAGE ON SCHEMA " + logs + " TO " + id(roles.Extract)); err != nil {
			return err
		}
		if err := exec("GRANT SELECT ON ALL TABLES IN SCHEMA " + logs + " TO " + id(roles.Extract)); err != nil {
			return err
		}
		// The extract role moves its own cursors, and nothing else in the log schema.
		var cursors bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"_log.extract_cursor").Scan(&cursors); err != nil {
			return err
		}
		if cursors {
			if err := exec("GRANT INSERT, UPDATE, DELETE ON " + logs + ".extract_cursor TO " + id(roles.Extract)); err != nil {
				return err
			}
		}
	}
	if roles.Readonly != "" {
		if err := exec("GRANT USAGE ON SCHEMA " + app + ", " + logs + " TO " + id(roles.Readonly)); err != nil {
			return err
		}
		if err := exec("GRANT SELECT ON ALL TABLES IN SCHEMA " + logs + " TO " + id(roles.Readonly)); err != nil {
			return err
		}
	}
	for _, t := range tables {
		table := app + "." + id(t.Name)
		if t.NoExtract && t.Logged() && roles.Extract != "" {
			if err := exec("REVOKE ALL ON " + logs + "." + id(t.Name+"_log") + " FROM " + id(roles.Extract)); err != nil {
				return err
			}
		}
		for _, r := range []string{roles.App, roles.Readonly} {
			if r != "" {
				if err := exec("REVOKE ALL ON " + table + " FROM " + id(r)); err != nil {
					return err
				}
			}
		}
		if t.Class == Internal {
			continue
		}
		if roles.App != "" {
			privs := "SELECT, INSERT, UPDATE"
			if t.Class == AppendOnly || t.Class == Journal {
				privs = "SELECT, INSERT" // never rewritten
			}
			if t.Deletable() {
				privs += ", DELETE"
			}
			if err := exec("GRANT " + privs + " ON " + table + " TO " + id(roles.App)); err != nil {
				return err
			}
		}
		if roles.Readonly != "" {
			// A column allowlist, so a secret column added later fails closed.
			rows, err := tx.Query(ctx, `SELECT attname FROM pg_attribute WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped ORDER BY attnum`, schema+"."+t.Name)
			if err != nil {
				return err
			}
			cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			var allowed string
			for _, c := range cols {
				if !slices.Contains(t.Secrets, c) {
					if allowed != "" {
						allowed += ", "
					}
					allowed += id(c)
				}
			}
			if allowed != "" {
				if err := exec("GRANT SELECT (" + allowed + ") ON " + table + " TO " + id(roles.Readonly)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// CheckManifest compares the manifest with the catalogue, and returns every disagreement:
//   - a table in the schema the manifest doesn't list, or the reverse
//   - a logged table with no log twin, or an unlogged one with a twin
//   - a secret column that is in a log twin
func CheckManifest(ctx context.Context, q Querier, tables []Table) ([]string, error) {
	var schema string
	if err := q.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND NOT c.relispartition`, schema)
	if err != nil {
		return nil, err
	}
	inSchema, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var problems []string
	listed := map[string]Table{}
	for _, t := range tables {
		listed[t.Name] = t
	}
	for _, name := range inSchema {
		if _, ok := listed[name]; !ok {
			problems = append(problems, "table "+name+" is not in the manifest")
		}
	}
	for _, t := range tables {
		if !slices.Contains(inSchema, t.Name) {
			problems = append(problems, "manifest table "+t.Name+" does not exist")
			continue
		}
		var twin bool
		if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{schema + "_log", t.Name + "_log"}.Sanitize()).Scan(&twin); err != nil {
			return nil, err
		}
		switch {
		case t.Logged() && !twin:
			problems = append(problems, "table "+t.Name+" is logged but has no log twin")
		case !t.Logged() && twin:
			problems = append(problems, "table "+t.Name+" is "+string(t.Class)+" but has a log twin")
		}
		for _, sec := range t.Secrets {
			var leaked bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3)`,
				schema+"_log", t.Name+"_log", sec).Scan(&leaked); err != nil {
				return nil, err
			}
			if leaked {
				problems = append(problems, "secret "+t.Name+"."+sec+" is in the log")
			}
		}
	}
	return problems, nil
}
