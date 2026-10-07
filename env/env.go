// Package env loads the product's .env (beside its go.mod) into the process environment.
//
// A developer convenience with one rule that makes it safe everywhere else:
// **a variable already set in the environment is never overwritten**. So a
// deployment that injects real configuration is unaffected by a file it does
// not have, `make db-migrate` still wins over a stale DATABASE_URL_UNPOOLED on
// disk, and a one-off `FOO=bar go run ./cmd/...` still overrides the file. The
// file supplies what is missing; it does not decide anything.
//
// That rule is why Load is called unconditionally rather than behind a
// development check. On Vercel there is no .env to find, and if there somehow
// were, every real variable is already set and would win.
//
// plinth adds a second rule (spec.md §1.5): **only the names the product
// declares are set.** A product's environment list is frozen, so a line naming
// anything else is reported rather than quietly configuring something.
package env

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Result is what Load did.
type Result struct {
	// Path is the file read, or empty when there was none.
	Path string
	// Set is how many variables it set.
	Set int
	// Ignored are the names in the file the product didn't declare, sorted.
	// They are never set; a caller logs them.
	Ignored []string
}

// Load reads the .env file beside go.mod and sets each declared variable that
// is not already present in the environment.
//
// Absent is not an error: the file holds credentials, is gitignored, and a
// checkout that has never been configured must still build, test and run.
func Load(declared ...string) (Result, error) {
	path, err := find()
	if err != nil {
		return Result{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, nil
		}
		return Result{}, err
	}
	defer func() { _ = f.Close() }()

	r := Result{Path: path}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		name, value, ok := parse(scanner.Text())
		if !ok {
			continue
		}
		if !slices.Contains(declared, name) {
			r.Ignored = append(r.Ignored, name)
			continue
		}
		// The whole safety property of this package, in one condition.
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return r, err
		}
		r.Set++
	}
	slices.Sort(r.Ignored)
	r.Ignored = slices.Compact(r.Ignored)
	return r, scanner.Err()
}

// find locates the .env beside go.mod, walking up from the working directory.
//
// Anchoring on go.mod rather than on the working directory matters twice. It
// makes `go run ./cmd/server` and `go test ./internal/...` find the
// same file, which is the point — a test needing credentials and a binary
// needing credentials should not read different ones. And it bounds the walk:
// stopping at the module root means a checkout nested under some other project
// can never reach up and read *its* .env, which would be a confusing way to
// leak configuration between unrelated repositories.
func find() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, ".env"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// No module root above us. Fall back to the working directory so
			// that a binary run from somewhere unexpected still behaves
			// predictably rather than searching the whole filesystem.
			cwd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			return filepath.Join(cwd, ".env"), nil
		}
		dir = parent
	}
}

// parse reads one line, reporting whether it carries an assignment.
//
// Deliberately literal: a value is taken as written, with no expansion of $VAR
// and no interpretation of backslash escapes. The file's only job is to hold
// tokens and connection strings, and both are full of characters that a clever
// parser would mangle.
//
// Inline comments are **not** stripped, for the same reason. `#` is ordinary
// inside a password or a URL fragment, and a parser that truncates at one
// produces a credential that is wrong in a way nothing reports — the connection
// simply fails to authenticate. A trailing comment is written on its own line
// instead.
func parse(line string) (name, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	// `export FOO=bar`, so a file can be both read here and sourced by a shell.
	line = strings.TrimPrefix(line, "export ")

	name, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	// Only a matched pair is a quote. An apostrophe inside an unquoted value is
	// part of it, and a value that begins with one but does not end with one is
	// more likely a password than a quoting mistake.
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return name, value, true
}
