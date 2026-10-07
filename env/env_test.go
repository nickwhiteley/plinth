package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line, name, value string
		ok                bool
	}{
		{line: `FOO=bar`, name: "FOO", value: "bar", ok: true},
		{line: `  FOO = bar  `, name: "FOO", value: "bar", ok: true},
		{line: `export FOO=bar`, name: "FOO", value: "bar", ok: true},
		{line: `FOO="bar baz"`, name: "FOO", value: "bar baz", ok: true},
		{line: `FOO='bar baz'`, name: "FOO", value: "bar baz", ok: true},
		{line: `FOO=`, name: "FOO", value: "", ok: true},

		// A connection string is the reason this package is careful.
		{
			line:  `DATABASE_URL=postgres://u:p@h:5432/db?sslmode=require`,
			name:  "DATABASE_URL",
			value: "postgres://u:p@h:5432/db?sslmode=require",
			ok:    true,
		},
		// Not a comment: `#` is ordinary inside a secret, and truncating here
		// would produce a credential that is wrong without anything saying so.
		{line: `TOKEN=abc#def`, name: "TOKEN", value: "abc#def", ok: true},
		// An unmatched quote is part of the value, not a quoting mistake.
		{line: `PW='sn`, name: "PW", value: "'sn", ok: true},
		{line: `PW=it's`, name: "PW", value: "it's", ok: true},
		// Values are literal: no expansion, no escapes.
		{line: `A=$B`, name: "A", value: "$B", ok: true},
		{line: `A=x\ny`, name: "A", value: `x\ny`, ok: true},

		{line: ``, ok: false},
		{line: `   `, ok: false},
		{line: `# a comment`, ok: false},
		{line: `  # indented comment`, ok: false},
		{line: `no equals sign`, ok: false},
		{line: `=novalue`, ok: false},
	}
	for _, c := range cases {
		name, value, ok := parse(c.line)
		if ok != c.ok {
			t.Errorf("parse(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if name != c.name || value != c.value {
			t.Errorf("parse(%q) = %q, %q; want %q, %q", c.line, name, value, c.name, c.value)
		}
	}
}

// The property everything else depends on: a real environment always wins.
func TestLoadDoesNotOverrideExisting(t *testing.T) {
	dir := moduleWithEnv(t, "SET_ALREADY=from-file\nNOT_SET=from-file\n")
	t.Setenv("SET_ALREADY", "from-environment")
	_ = os.Unsetenv("NOT_SET")
	t.Chdir(dir)

	if r, err := Load("SET_ALREADY", "NOT_SET"); err != nil {
		t.Fatalf("Load: %v", err)
	} else if r.Set != 1 {
		t.Errorf("set = %d, want 1 (only the absent one)", r.Set)
	}
	if got := os.Getenv("SET_ALREADY"); got != "from-environment" {
		t.Errorf("SET_ALREADY = %q, want the environment to win", got)
	}
	if got := os.Getenv("NOT_SET"); got != "from-file" {
		t.Errorf("NOT_SET = %q, want %q", got, "from-file")
	}
	t.Cleanup(func() { _ = os.Unsetenv("NOT_SET") })
}

// An empty value in the file counts as set, and so is not overwritten by it.
func TestLoadTreatsEmptyAsSet(t *testing.T) {
	dir := moduleWithEnv(t, "EMPTY_ALREADY=from-file\n")
	t.Setenv("EMPTY_ALREADY", "")
	t.Chdir(dir)

	if r, err := Load("EMPTY_ALREADY"); err != nil {
		t.Fatalf("Load: %v", err)
	} else if r.Set != 0 {
		t.Errorf("set = %d, want 0", r.Set)
	}
	if got := os.Getenv("EMPTY_ALREADY"); got != "" {
		t.Errorf("EMPTY_ALREADY = %q, want it left empty", got)
	}
}

// A checkout that has never been configured must still run.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	r, err := Load("ANYTHING")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Set != 0 || r.Path != "" {
		t.Errorf("Load() = %+v; want nothing", r)
	}
}

// Anchoring on go.mod is what stops a nested checkout reading the .env of
// whatever it happens to sit inside.
func TestLoadFindsModuleRootFromSubdirectory(t *testing.T) {
	root := moduleWithEnv(t, "FROM_ROOT=yes\n")
	outer := filepath.Dir(root)
	if err := os.WriteFile(filepath.Join(outer, ".env"), []byte("FROM_OUTER=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("FROM_ROOT")
	_ = os.Unsetenv("FROM_OUTER")
	t.Chdir(sub)

	r, err := Load("FROM_ROOT", "FROM_OUTER")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(root, ".env"); r.Path != want {
		t.Errorf("path = %q, want %q", r.Path, want)
	}
	if os.Getenv("FROM_ROOT") != "yes" {
		t.Error("did not read the module-root .env")
	}
	if _, found := os.LookupEnv("FROM_OUTER"); found {
		t.Error("walked past the module root and read an unrelated .env")
	}
	t.Cleanup(func() { _ = os.Unsetenv("FROM_ROOT") })
}

// The product's environment list is frozen (plinth spec §1.5): a name the product didn't declare is
// reported and never set, so a stale or mistaken line can't quietly configure anything.
func TestLoadSetsOnlyTheNamesItIsGiven(t *testing.T) {
	dir := moduleWithEnv(t, "DECLARED=yes\nSTRAY=no\nANOTHER_STRAY=no\n")
	_ = os.Unsetenv("DECLARED")
	_ = os.Unsetenv("STRAY")
	_ = os.Unsetenv("ANOTHER_STRAY")
	t.Chdir(dir)

	r, err := Load("DECLARED")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Set != 1 || os.Getenv("DECLARED") != "yes" {
		t.Errorf("Load = %+v, DECLARED = %q", r, os.Getenv("DECLARED"))
	}
	if _, found := os.LookupEnv("STRAY"); found {
		t.Error("an undeclared name was set")
	}
	if len(r.Ignored) != 2 || r.Ignored[0] != "ANOTHER_STRAY" || r.Ignored[1] != "STRAY" {
		t.Errorf("Ignored = %v, want the undeclared names, sorted", r.Ignored)
	}
	t.Cleanup(func() { _ = os.Unsetenv("DECLARED") })
}

// moduleWithEnv builds a temporary module root holding the given .env, nested
// one directory down so a test can also write a file above it.
func moduleWithEnv(t *testing.T, contents string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "module")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
