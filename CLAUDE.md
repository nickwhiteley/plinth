# CLAUDE.md

This file guides Claude Code (claude.ai/code) when working in this repository.

## Orientation

- **`spec.md` is authoritative** and must stay complete enough to rebuild the module. Read it
  before any design decision.
- **`README.md`** covers setup, running and the build state.
- The build plan lives in Furniture Magic's `PROGRESS.md`, Phase 1
  (`../furnituredesigner/PROGRESS.md`). Its decisions log records divergences.
- **Bloomprint** (`../bloomprint/api/internal/`) is the source of the lifted packages. It is
  never changed by a lift.

## Working rules

- **All work is on a branch,** never on `main`. Merge by pull request, which gets the automated
  AI review (`PR-REVIEW.md`).
- **TDD:** write the failing test first. A lifted package arrives with its Bloomprint tests
  passing.
- **Spec first:** a package's contract is in `spec.md` before its code is merged.
- **Keep `README.md` true:** a change to setup, commands or configuration updates it in the same
  branch, and anything unbuilt is marked *planned*.

## Invariants: hold every change to these

- **A library, not a framework.** No goroutine, connection or environment read unless the
  product asks. The product owns its router and process.
- **No prose.** Errors are `code.Error` values: a stable code and parameters. Email is the only
  rendered text, from message catalogues.
- **No product names.** Never name a schema, a database role, a product or a domain concept.
- **Each package owns its storage:** a `Store` interface, `mem` and `pg` implementations, and a
  conformance suite that holds them equal.
- **The data layer** follows Furniture Magic's data-model §1: typed columns, UUIDv7 keys,
  bounded `CHECK`s, explicit constraint names, every table through `shadow()` unless registered
  exempt, a `COMMENT` on every column, no extensions.
- **A product never alters a `plinth` table.** It extends one with its own table, keyed
  one-to-one.
- **Released migrations are additive only.** The warehouse reads these tables.
- **Secrets** are stored only as hashes or ciphertext, declared in the table manifest, and never
  logged.

## Commands

```bash
make test       # unit tests and the conformance suites against the in-memory stores
make lint       # go vet and gofmt
make db-up      # Postgres 18 in Docker on :5435
make db-down
make test-db    # everything against Postgres 18 (needs make db-up)
```
