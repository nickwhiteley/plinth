package dataapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/dataapi"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/migrations"
	"github.com/nickwhiteley/plinth/pgtest"
)

var ctx = context.Background()

// setup gives an extractor connected as the real extract role, and the superuser's pool for
// writing the rows to extract.
func setup(t *testing.T) (*dataapi.Extractor, *pgtest.DB) {
	t.Helper()
	d := pgtest.New(t)
	pgtest.EnsureRoles(t, d, migrations.Tables)
	pool, err := pgxpool.New(ctx, pgtest.URLAs(t, d, pgtest.Roles.Extract))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return dataapi.New(pool, d.Schema), d
}

func tier(t *testing.T, d *pgtest.DB, key string, order int) {
	t.Helper()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO tier (id, key, name, sort_order) VALUES ($1, $2, $2, $3)`, ids.New(), key, order); err != nil {
		t.Fatal(err)
	}
}

func TestTablesListsWhatTheRoleMayRead(t *testing.T) {
	e, _ := setup(t)
	tables, err := e.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]dataapi.Table{}
	for _, tb := range tables {
		got[tb.Name] = tb
	}
	if _, ok := got["account"]; !ok || got["account"].Description == "" {
		t.Errorf("account isn't listed with its description: %+v", tables)
	}
	for _, hidden := range []string{"app_setting", "session", "identity_token", "communication_log", "log_exclusion", "extract_cursor"} {
		if _, ok := got[hidden]; ok {
			t.Errorf("%s is listed", hidden)
		}
	}
}

func TestWindowPagesInOrder(t *testing.T) {
	e, d := setup(t)
	for i, k := range []string{"free", "plus", "pro"} {
		tier(t, d, k, i+1)
	}
	p, err := e.Window(ctx, "tier", dataapi.Cursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 2 || !p.More {
		t.Fatalf("first page: %d rows, more %v", len(p.Rows), p.More)
	}
	var row map[string]any
	if err := json.Unmarshal(p.Rows[0], &row); err != nil || row["key"] != "free" || row["op"] != "I" || row["sort_order"] != float64(1) {
		t.Errorf("a row: %s", p.Rows[0])
	}
	p2, err := e.Window(ctx, "tier", p.Next, 2)
	if err != nil || len(p2.Rows) != 1 || p2.More {
		t.Fatalf("second page: %d rows, more %v, %v", len(p2.Rows), p2.More, err)
	}
	_ = json.Unmarshal(p2.Rows[0], &row)
	if row["key"] != "pro" {
		t.Errorf("the second page starts at %v", row["key"])
	}
	// Past the end: nothing, and the cursor stays.
	p3, _ := e.Window(ctx, "tier", p2.Next, 2)
	if len(p3.Rows) != 0 || p3.Next != p2.Next {
		t.Errorf("past the end: %+v", p3)
	}
	for _, bad := range []struct {
		table string
		limit int
		want  error
	}{
		{"app_setting", 10, dataapi.ErrUnknownTable}, // withheld from the extract role
		{"session", 10, dataapi.ErrUnknownTable},     // has no log
		{"nope", 10, dataapi.ErrUnknownTable},
		{"Robert'); DROP TABLE tier;--", 10, dataapi.ErrUnknownTable},
		{"tier", 0, dataapi.ErrLimitInvalid},
		{"tier", dataapi.MaxLimit + 1, dataapi.ErrLimitInvalid},
	} {
		if _, err := e.Window(ctx, bad.table, dataapi.Cursor{}, bad.limit); !errors.Is(err, bad.want) {
			t.Errorf("Window(%q, %d) = %v", bad.table, bad.limit, err)
		}
	}
}

// Spike 0.1's proof, kept: a long transaction that commits after a shorter one is still extracted,
// in txid order, and nothing at or above an open transaction's txid is read early.
func TestALongTransactionIsNeverSkipped(t *testing.T) {
	e, d := setup(t)
	long, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = long.Rollback(ctx) }()
	if _, err := long.Exec(ctx, `INSERT INTO tier (id, key, name, sort_order) VALUES ($1, 'long', 'long', 1)`, ids.New()); err != nil {
		t.Fatal(err)
	}
	tier(t, d, "short", 2) // commits now, with a later txid

	early, err := e.Window(ctx, "tier", dataapi.Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(early.Rows) != 0 {
		t.Fatalf("read %d rows above an open transaction: the short one would be acknowledged and the long one skipped", len(early.Rows))
	}
	if err := long.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Window(ctx, "tier", early.Next, 100)
	var keys []string
	for _, r := range after.Rows {
		var row map[string]any
		_ = json.Unmarshal(r, &row)
		keys = append(keys, row["key"].(string))
	}
	if strings.Join(keys, ",") != "long,short" {
		t.Errorf("after the long transaction committed: %v, want long then short", keys)
	}
}

func TestAckIsForwardOnlyAndResetForgets(t *testing.T) {
	e, d := setup(t)
	for i, k := range []string{"free", "plus"} {
		tier(t, d, k, i+1)
	}
	p1, _ := e.Window(ctx, "tier", dataapi.Cursor{}, 1)
	p2, _ := e.Window(ctx, "tier", p1.Next, 1)
	if err := e.Ack(ctx, "tier", p2.Next, 2); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	stored := func() *dataapi.Cursor {
		ts, _ := e.Tables(ctx)
		for _, tb := range ts {
			if tb.Name == "tier" {
				return tb.Cursor
			}
		}
		return nil
	}
	if c := stored(); c == nil || *c != p2.Next {
		t.Fatalf("stored %+v, want %+v", c, p2.Next)
	}
	// Going back is refused; Reset is how.
	if err := e.Ack(ctx, "tier", p1.Next, 1); !errors.Is(err, dataapi.ErrCursorInvalid) {
		t.Errorf("acknowledging backwards = %v", err)
	}
	if err := e.Ack(ctx, "tier", dataapi.Cursor{}, 0); !errors.Is(err, dataapi.ErrCursorInvalid) {
		t.Errorf("acknowledging nothing = %v", err)
	}
	if err := e.Reset(ctx, "tier"); err != nil {
		t.Fatal(err)
	}
	if c := stored(); c != nil {
		t.Errorf("after Reset: %+v", c)
	}
	if err := e.Ack(ctx, "app_setting", p2.Next, 1); !errors.Is(err, dataapi.ErrUnknownTable) {
		t.Errorf("acknowledging a withheld log = %v", err)
	}
}

func TestHandler(t *testing.T) {
	e, d := setup(t)
	tier(t, d, "free", 1)
	srv := httptest.NewServer(http.StripPrefix("/data", dataapi.Handler(e, nil)))
	defer srv.Close()
	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if status, body := get("/data/extract"); status != 200 || len(body["tables"].([]any)) == 0 {
		t.Errorf("list: %d %v", status, body)
	}
	status, page := get("/data/extract/tier?limit=10")
	if status != 200 || len(page["rows"].([]any)) != 1 {
		t.Fatalf("page: %d %v", status, page)
	}
	next := page["next"].(map[string]any)
	ack := `{"txid": "` + next["txid"].(string) + `", "log_id": "` + next["log_id"].(string) + `", "rows": 1}`
	resp, err := http.Post(srv.URL+"/data/extract/tier/ack", "application/json", strings.NewReader(ack))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("ack: %v %v", resp.StatusCode, err)
	}
	if status, _ := get("/data/extract/tier?after_txid=" + next["txid"].(string) + "&after_log_id=" + next["log_id"].(string)); status != 200 {
		t.Errorf("a page after the cursor: %d", status)
	}
	for path, want := range map[string]int{
		"/data/extract/app_setting":            404,
		"/data/extract/tier?limit=0":           400,
		"/data/extract/tier?after_txid=x":      400,
		"/data/extract/tier?after_log_id=nope": 400,
	} {
		if status, body := get(path); status != want || !strings.HasPrefix(body["code"].(string), "dataapi.") {
			t.Errorf("%s: %d %v, want %d with a code", path, status, body, want)
		}
	}
	resp, _ = http.Post(srv.URL+"/data/extract/tier/reset", "application/json", nil)
	if resp.StatusCode != 204 {
		t.Errorf("reset: %d", resp.StatusCode)
	}
}

func TestParseCursor(t *testing.T) {
	if c, err := dataapi.ParseCursor("", ""); err != nil || !c.IsZero() {
		t.Errorf("the start: %+v, %v", c, err)
	}
	u := ids.New()
	if c, err := dataapi.ParseCursor("123", u.String()); err != nil || c.TxID != "123" || c.LogID != u {
		t.Errorf("a cursor: %+v, %v", c, err)
	}
	for _, bad := range [][2]string{{"-1", u.String()}, {"12", ""}, {"", u.String()}, {"1e3", u.String()}} {
		if _, err := dataapi.ParseCursor(bad[0], bad[1]); !errors.Is(err, dataapi.ErrCursorInvalid) {
			t.Errorf("ParseCursor(%q, %q) = %v", bad[0], bad[1], err)
		}
	}
}
