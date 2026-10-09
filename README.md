# plinth

The Go module Nick Whiteley's products share: identity and accounts, system roles and
permissions, billing, feature flags, quotas and usage, encrypted settings, email with i18n,
alerting, blob storage, the Postgres shadow audit logs, and the data output API.

It's named for the base a piece of furniture stands on. Its first consumer is Furniture Magic,
and its packages are lifted from Bloomprint with their tests. **`spec.md` is authoritative.**

## Status

**Early build.** The module, CI and the specification exist, and so do these packages:

| Package | Status |
|---|---|
| `code`: error values with a stable code and parameters | built |
| `ids`: UUIDv7, prefixed Crockford ids, secret tokens | built |
| `identity`: the local provider (passwords, reset and verification links, Google sign-in) | built, with in-memory and Postgres stores |
| `account`: accounts, profiles and sessions | built, with in-memory and Postgres stores |
| `env`: the `.env` loader, for declared names only | built |
| `settings`: declared, encrypted settings, the live snapshot and its write path | built, with in-memory and Postgres stores; the admin handler is the product's |
| `alert`: errors from the log, rationed, redacted and mailed | built |
| `actor`: who is acting, for attribution | built |
| `flags`: tiers and feature flags | built, with in-memory and Postgres stores |
| `quotas`: limits by tier, with overrides | built, with in-memory and Postgres stores |
| `usage`: the quota day and the check | built |
| `db`: the pool, transactions, migrations, the manifest and grants | built |
| `migrations`: plinth's schema, with the shadow log | built |
| `shadowlog`: the boot checks | built |
| `pgtest`, `fixture`: Postgres test schemas and fixtures | built |
| `email`: kinds, Postmark, the communication log, and rendering in the recipient's locale | built, with in-memory and Postgres stores |
| `dataapi`: extraction of the shadow logs by `(txid, log_id)` cursor, and its handler | built |
| `billing`: prices, checkouts and subscriptions, the provider port and a stub provider | built, with in-memory and Postgres stores. No proration, pause or dunning yet |
| `rbac`: system roles and permissions, with the last-holder guard | built, with in-memory and Postgres stores |
| `blob`: object storage (Vercel Blob, a local directory, memory; S3 or another is planned) | done |

## Using it

A product imports the packages it needs and composes them in its `main`. Nothing runs, connects
or reads the environment until the product asks it to. While the repository is private, Go needs
to be told it isn't a public module:

```bash
go env -w GOPRIVATE=github.com/nickwhiteley/*
go get github.com/nickwhiteley/plinth@<version>
```

## Checking migrations at boot

`db.Pending` tells a product's server, running as its runtime role, whether every migration has
been applied. It reads only, applies nothing, takes no lock and needs no owner login:

```go
st, err := db.Pending(ctx, pool, migrations.Plinth, productStream)
if err != nil { /* the check itself failed, e.g. no SELECT on a version table */ }
if !st.OK() { log.Error("migrations", "pending", st.Pending(), "err", st.Err()) }
```

`Status.Streams` reports, per stream, what is `Pending`, `Unknown` (the code is older than the
database), `Changed` (a checksum differs) and `Missing` (no version table at all). `Err()` wraps
them in `db.migration_pending`, `db.migration_unknown`, `db.migration_changed` and
`db.migration_table_missing`. It relies on `db.Grant`, which now gives the runtime role `SELECT`
(only) on the manifest's `internal` tables, so re-run `Grant` after upgrading. See `spec.md` §4.

## Developing

Prerequisites: Go 1.26+, and Docker for the Postgres tests.

```bash
make test       # unit tests
make lint       # go vet and gofmt
make db-up      # Postgres 18 in Docker on localhost:5435 (beside a product's on 5434)
make db-down    # stop it
make test-db    # everything against Postgres 18: stores, migrations, role separation (needs make db-up)
```

All work is on a branch and merges by pull request. CI runs `make lint` and `make test`, and an
automated review (`PR-REVIEW.md`) comments on every pull request into `main`. It needs the
repository secret `OLLAMA_API_KEY`.
