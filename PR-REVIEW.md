# PR-REVIEW.md

Configuration for the automated PR review workflow powered by
[pr-reviewer](https://github.com/nickwhiteley/pr-reviewer)
(`.github/workflows/pr-review.yml`). Each `##` section below is read by the
tool — do not rename them.

## Who owns the repo
Nick Whiteley — solo developer.

## Context and intent
plinth is the Go module Nick Whiteley's products share: identity and
accounts, billing, feature flags, quotas and usage, encrypted settings, email
with i18n, alerting, the Postgres shadow audit logs, and the data output API
the reporting warehouse reads. Its packages are lifted from Bloomprint with
their tests. It is a library that a product's main composes, not a framework.
Stack: Go 1.26, pgx/v5, PostgreSQL 18.

The invariants the agents should hold a diff to are in `CLAUDE.md`. The
resolved design is in `spec.md`, which is authoritative.

## Production status
Pre-implementation: the scaffold and specification. Its first consumer,
Furniture Magic, is not yet deployed. Products deploy to Vercel with Neon
Postgres.

## Connected systems
- PostgreSQL 18, in each product's own database: plinth's tables live in the
  product's app schema, audited into its log schema, under the product's
  segregated roles (owner, migrate, runtime, extract, read-only)
- Postmark (email), Google OAuth sign-in, the billing provider
- A reporting warehouse reading `_log` tables and `<app>_extract` views through
  the data API under a published data contract

## Intended audience
Developers of the products that import it. Through them: every person who
signs up to, signs in to or pays for one of those products.

## Audit level
Medium

## Security level
High — credentials, sessions, password reset and verification tokens, Google
sign-in, encrypted settings holding every product's provider keys, billing,
and an audit log the runtime role must be unable to write.

## PR Hygiene
- [x] H001 README.md is present and up to date
- [ ] H002 CODEOWNERS exists
- [ ] H003 All runtime and dependency versions are up to date
- [ ] H006 Dev container definitions exist and are appropriate

## Code Reviews

| Plugin            | Subagent           | Paths | Additional |
| ----------------- | ------------------ | ----- | ---------- |
| voltagent-qa-sec  | code-reviewer      |  | hold the change to CLAUDE.md's invariants: a library not a framework (no goroutines, connections or environment reads unless the product asks); errors are code.Error values, never prose; no product, schema or role names; lifted packages keep their Bloomprint tests |
| voltagent-qa-sec  | security-auditor   | `!*_test.go` | tokens and passwords are stored only as hashes and never logged; every setting value is encrypted; secret columns are declared in the table manifest so no product's read-only or extract role can read them; no secret reaches a log, an alert or the warehouse |
| voltagent-qa-sec  | architect-reviewer | `spec.md`, `!*_test.go` | each package owns its Store interface with mem and pg implementations held equal by a conformance suite; a product never alters a plinth table; released migrations are additive only; a minor version never changes a code's meaning or removes a setting |
| voltagent-data-ai | postgres-pro       | `*.sql`, `**/pg/**`, `db/`, `shadowlog/` | typed columns not JSON, UUIDv7 keys, explicit constraint names, numeric bounds on every CHECK, every table created via shadow() unless registered exempt, a COMMENT on every column, no schema or role names in migrations, no extensions |

## Excluded Paths

- (none yet)

## Running locally

Install the tool once:

```sh
go install github.com/nickwhiteley/pr-reviewer/cmd/review@latest
```

Run against the current branch (compares to `main`):

```sh
review
```

Run against a specific GitHub PR:

```sh
review --pr <number>
```

## CI workflow

`.github/workflows/pr-review.yml` runs `review` on every pull request into
`main` and posts the result as a comment per agent, plus inline comments on the
lines findings map to. A non-zero exit fails the check, which is what a
verified critical or high finding produces.
