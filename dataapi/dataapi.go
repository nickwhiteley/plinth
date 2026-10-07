// Package dataapi is the warehouse's way in (spec.md §14): incremental extraction of the shadow
// logs, read as the extract role, which can see the log schema and nothing else. Folded in from
// the standalone data-api, and rebuilt on Furniture Magic data-model §10.3's cursor.
//
// **The cursor is (txid, log_id), read below the oldest transaction still open.** A time window
// or a sequence skips a long transaction that commits after a shorter one; reading only below
// pg_snapshot_xmin never does, because nothing at or above an open transaction's txid is returned
// until it has committed (proved by spike 0.1).
//
// **The consumer acknowledges.** Window returns a page past a cursor; Ack moves the stored cursor
// only once the consumer has kept that page, so a crash re-reads a page rather than losing one.
package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
)

// Cursor is a position in one table's log: the last row read, or the zero Cursor for the start.
type Cursor struct {
	// TxID is the transaction, as a decimal string (xid8 doesn't fit JSON's numbers safely).
	TxID  string   `json:"txid"`
	LogID ids.UUID `json:"log_id"`
}

// IsZero reports the start of a log.
func (c Cursor) IsZero() bool { return c.TxID == "" || c.TxID == "0" }

// MarshalJSON writes the log id as a uuid string.
func (c Cursor) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		TxID  string `json:"txid"`
		LogID string `json:"log_id"`
	}{c.TxID, c.LogID.String()})
}

// Table is a log the extract role may read.
type Table struct {
	Name        string `json:"table"`
	Description string `json:"description"`
	// Cursor is the stored cursor, or nil if this log has never been acknowledged.
	Cursor *Cursor `json:"cursor,omitempty"`
}

// Page is one window of a log.
type Page struct {
	Table string `json:"table"`
	// Rows are the log rows, each as Postgres's row_to_json, so numbers, uuids and times keep
	// their types. Every row carries op, txid, log_id, logged_at and modified_by.
	Rows []json.RawMessage `json:"rows"`
	// Next is the cursor after the last row, to acknowledge once the page is kept, and to pass
	// back for the next page.
	Next Cursor `json:"next"`
	// More reports that the page was full, so more rows may follow now.
	More bool `json:"more"`
}

// MaxLimit is the largest page.
const MaxLimit = 10000

var (
	// ErrUnknownTable is a log that doesn't exist, or that the extract role may not read.
	ErrUnknownTable = code.New("dataapi.unknown_table")
	// ErrCursorInvalid is a cursor that doesn't parse, or one ahead of the stored cursor on Ack.
	ErrCursorInvalid = code.New("dataapi.cursor_invalid")
	// ErrLimitInvalid is a page size outside 1–MaxLimit.
	ErrLimitInvalid = code.New("dataapi.limit_invalid")
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Extractor reads the logs. Its pool connects as the extract role. appSchema is the product's app
// schema, which it has as configuration: the extract role has no USAGE on it, so it can't be
// found from the connection (current_schema() is null for a schema a role can't use), and the
// log schema is appSchema_log.
type Extractor struct {
	q    db.Querier
	logs string
}

func New(q db.Querier, appSchema string) *Extractor {
	return &Extractor{q: q, logs: appSchema + "_log"}
}

func (e *Extractor) logSchema(context.Context) (string, error) { return e.logs, nil }

// Tables lists the logs the extract role may read, with their descriptions and stored cursors. A
// table whose log is withheld (the manifest's NoExtract) isn't listed.
func (e *Extractor) Tables(ctx context.Context) ([]Table, error) {
	logs, err := e.logSchema(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := e.q.Query(ctx, `
		SELECT left(c.relname, -4), coalesce(obj_description(c.oid, 'pg_class'), ''), x.last_txid::text, x.last_log_id
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  LEFT JOIN `+pgx.Identifier{logs, "extract_cursor"}.Sanitize()+` x ON x.table_name = left(c.relname, -4)
		 WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND c.relname LIKE '%\_log'
		   AND has_table_privilege(c.oid, 'SELECT')
		 ORDER BY 1`, logs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Table, error) {
		var t Table
		var txid *string
		var logID *ids.UUID
		if err := r.Scan(&t.Name, &t.Description, &txid, &logID); err != nil {
			return t, err
		}
		if txid != nil && logID != nil {
			t.Cursor = &Cursor{TxID: *txid, LogID: *logID}
		}
		return t, nil
	})
}

// readable checks the table names a log the role may read, and returns its qualified name.
func (e *Extractor) readable(ctx context.Context, table string) (string, string, error) {
	if !nameRe.MatchString(table) {
		return "", "", ErrUnknownTable.With("table", table)
	}
	logs, err := e.logSchema(ctx)
	if err != nil {
		return "", "", err
	}
	var ok bool
	err = e.q.QueryRow(ctx, `SELECT coalesce(has_table_privilege(to_regclass($1), 'SELECT'), false)`,
		pgx.Identifier{logs, table + "_log"}.Sanitize()).Scan(&ok)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", ErrUnknownTable.With("table", table)
	}
	return logs, pgx.Identifier{logs, table + "_log"}.Sanitize(), nil
}

// Window returns up to limit rows of a table's log after a cursor, in (txid, log_id) order, and
// only from transactions below the oldest one still open.
func (e *Extractor) Window(ctx context.Context, table string, after Cursor, limit int) (Page, error) {
	if limit < 1 || limit > MaxLimit {
		return Page{}, ErrLimitInvalid.With("max", strconv.Itoa(MaxLimit))
	}
	_, qualified, err := e.readable(ctx, table)
	if err != nil {
		return Page{}, err
	}
	txid := after.TxID
	if after.IsZero() {
		txid = "0"
	}
	if _, err := strconv.ParseUint(txid, 10, 64); err != nil {
		return Page{}, ErrCursorInvalid
	}
	rows, err := e.q.Query(ctx, `SELECT row_to_json(l)::text, l.txid::text, l.log_id FROM `+qualified+` l
		WHERE (l.txid, l.log_id) > ($1::xid8, $2::uuid) AND l.txid < pg_snapshot_xmin(pg_current_snapshot())
		ORDER BY l.txid, l.log_id LIMIT $3`, txid, after.LogID, limit)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	p := Page{Table: table, Rows: []json.RawMessage{}, Next: after}
	for rows.Next() {
		var row string
		var c Cursor
		if err := rows.Scan(&row, &c.TxID, &c.LogID); err != nil {
			return Page{}, err
		}
		p.Rows = append(p.Rows, json.RawMessage(row))
		p.Next = c
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	p.More = len(p.Rows) == limit
	return p, nil
}

// Ack stores the cursor once the consumer has kept the rows up to it, adding rows to the total. A
// cursor behind the stored one is refused: acknowledging is forward-only, and Reset goes back.
func (e *Extractor) Ack(ctx context.Context, table string, to Cursor, rows int) error {
	logs, _, err := e.readable(ctx, table)
	if err != nil {
		return err
	}
	if to.IsZero() || rows < 0 {
		return ErrCursorInvalid
	}
	if _, err := strconv.ParseUint(to.TxID, 10, 64); err != nil {
		return ErrCursorInvalid
	}
	tag, err := e.q.Exec(ctx, `INSERT INTO `+pgx.Identifier{logs, "extract_cursor"}.Sanitize()+` AS x (table_name, last_txid, last_log_id, rows_total)
		VALUES ($1, $2::xid8, $3, $4)
		ON CONFLICT (table_name) DO UPDATE SET last_txid = EXCLUDED.last_txid, last_log_id = EXCLUDED.last_log_id,
		  rows_total = x.rows_total + EXCLUDED.rows_total, updated_at = now()
		  WHERE (EXCLUDED.last_txid, EXCLUDED.last_log_id) >= (x.last_txid, x.last_log_id)`, table, to.TxID, to.LogID, rows)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCursorInvalid
	}
	return nil
}

// Reset forgets a table's cursor, so the next extraction starts from the beginning of what the
// log still holds.
func (e *Extractor) Reset(ctx context.Context, table string) error {
	logs, _, err := e.readable(ctx, table)
	if err != nil {
		return err
	}
	_, err = e.q.Exec(ctx, `DELETE FROM `+pgx.Identifier{logs, "extract_cursor"}.Sanitize()+` WHERE table_name = $1`, table)
	return err
}

// ParseCursor reads a cursor from its two parts, as the handler receives them. Both empty is the
// start of the log.
func ParseCursor(txid, logID string) (Cursor, error) {
	if txid == "" && logID == "" {
		return Cursor{}, nil
	}
	if _, err := strconv.ParseUint(txid, 10, 64); err != nil {
		return Cursor{}, ErrCursorInvalid
	}
	u, err := ids.ParseUUID(logID)
	if err != nil {
		return Cursor{}, ErrCursorInvalid
	}
	return Cursor{TxID: txid, LogID: u}, nil
}

// IsUnknown reports an error that a handler answers with 404.
func IsUnknown(err error) bool { return errors.Is(err, ErrUnknownTable) }
