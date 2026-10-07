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
| `identity`: the local provider (passwords, reset and verification links, Google sign-in) | built, with its in-memory store; the Postgres store is planned (1.5) |
| `account`: accounts, profiles and sessions | built, with its in-memory store; the Postgres store is planned (1.5) |
| `env`: the `.env` loader, for declared names only | built |
| `settings`: declared, encrypted settings, the live snapshot and its write path | built, with its in-memory store; the Postgres store and the admin handler are planned (1.5, `rbac`) |
| `alert`: errors from the log, rationed, redacted and mailed | built; the mail sink is planned (1.5) |
| `actor`: who is acting, for attribution | built |
| `flags`, `quotas`, `usage` | planned (1.4) |
| `email`, `shadowlog`, `db`, `storetest` | planned (1.5) |
| `dataapi` | planned (1.6) |
| `billing` | planned (Furniture Magic 8.3) |
| `rbac`: system roles and permissions | planned (Furniture Magic 8.1) |
| `blob`: object storage (Vercel Blob, S3 or another) | planned (Furniture Magic 6.3) |

## Using it

A product imports the packages it needs and composes them in its `main`. Nothing runs, connects
or reads the environment until the product asks it to. While the repository is private, Go needs
to be told it isn't a public module:

```bash
go env -w GOPRIVATE=github.com/nickwhiteley/*
go get github.com/nickwhiteley/plinth@<version>
```

## Developing

Prerequisites: Go 1.26+, and Docker for the Postgres tests.

```bash
make test       # unit tests
make lint       # go vet and gofmt
make db-up      # Postgres 18 in Docker on localhost:5435 (beside a product's on 5434)
make db-down    # stop it
make test-db    # planned: the conformance suites and role checks against Postgres
```

All work is on a branch and merges by pull request. CI runs `make lint` and `make test`, and an
automated review (`PR-REVIEW.md`) comments on every pull request into `main`. It needs the
repository secret `OLLAMA_API_KEY`.
