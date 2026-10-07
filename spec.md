# plinth: specification

`github.com/nickwhiteley/plinth` is the Go module Nick Whiteley's products share. It covers
identity, accounts, system roles and permissions, billing, flags, quotas, usage, settings, email,
alerting, blob storage, the shadow audit logs and the data output API. It's named for the base a piece of furniture stands on: it's what
products are built on, and none of it is any one product's business.

**This file is authoritative,** and complete enough to rebuild the module. A product's own spec
pins a `plinth` version and states only the contract it relies on, never the internals.

The first consumer is Furniture Magic (`github.com/nickwhiteley/furnituremagic`). Its
`spec.md` §16 and `specs/data-model.md` are where most of these decisions were first made.
Bloomprint (`gardendesigner`) is the source of most of the code. It migrates onto `plinth` on
its own schedule.

## 1. Principles

1. **Lifted, not rewritten.** Packages come from Bloomprint **with their tests**, when a
   product first needs them. Bloomprint isn't changed by the lift.
2. **A library, not a framework.** A product's `main` composes `plinth`'s packages:
   - It owns its router, its middleware order and its process.
   - `plinth` hands it `http.Handler`s to mount, stores to construct, and functions to call.
   - Nothing in `plinth` starts a goroutine, opens a connection or reads the environment
     unless the product asks it to.
3. **The product's rules hold here too.** Everything in Furniture Magic's data-model §1 applies
   to `plinth`'s tables:
   - typed columns, not JSON
   - UUIDv7 keys
   - `CHECK`s with upper bounds
   - explicit constraint names
   - Postgres 18 with no extensions
   - every table audited by the shadow logs unless it's registered as exempt
4. **No prose.** Errors, refusals and events are **codes with parameters**. The only text
   `plinth` renders is email, from message catalogues in the recipient's locale (§9).
5. **No configuration in the environment.** The only environment `plinth` reads is through
   `env`, the `.env` loader, and only for names the product passes it. Everything that can move
   is a declared, encrypted setting (§8).
6. **No product names.** `plinth` never names a schema, a database role, a product or a domain
   concept. A product passes them in.

## 2. Packages

| Package | From Bloomprint | What it is | Furniture Magic task |
|---|---|---|---|
| `env` | `internal/env` | The `.env` loader. It never overrides the environment, and reads values literally (no expansion or quotes). | 1.3 |
| `settings` | `internal/settings` | Declared, encrypted settings rows, reconciled at boot, a three-minute snapshot, and the `/admin/settings` handler | 1.3 |
| `alert` | `internal/alert` | Errors reported from the log handler, rate-limited and redacted | 1.3 |
| `identity` | `internal/auth`, split | The provider interface, the local provider (password, Google, reset, verification), sessions and deletion events | 1.2 |
| `account` | `internal/auth`, split | Accounts and their profile cache, keyed to an identity by (issuer, subject) | 1.2 |
| `flags` | `internal/flags` | Feature flags by tier, with per-account overrides | 1.4 |
| `quotas` | `internal/quotas` | Limits by tier with per-account overrides, and reservation | 1.4 |
| `usage` | `internal/usage` | Metered usage counted against quotas | 1.4 |
| `billing` | `internal/billing` | Tiers, prices, subscriptions and the stub provider | Furniture Magic 8.3 |
| `email` | `internal/email` | Sending (Postmark, and a logging sender for development), kinds, the communication log, and i18n | 1.5 |
| `shadowlog` | `internal/db` migrations | The `shadow()` migration helper, the log trigger, the exclusion registry and the boot checks | 1.5 |
| `db` | `internal/db` | Opening the pool, the transaction helper that sets the actor, migration running (§4) and the table manifest | 1.5 |
| `code` | (new) | Error values: a stable code and string parameters (§9) | 1.1 |
| `storetest` | `internal/store/storetest` | Per-schema test databases and the conformance-suite harness | 1.5 |
| `dataapi` | the `data-api` repository | The extraction routes over the `(txid, log_id)` cursor | 1.6 |
| `rbac` | Bloomprint's admin role, generalised | System permissions declared in code and reconciled at boot, roles as data, role grants to accounts, and the last-holder guard | Furniture Magic 8.1 |
| `blob` | `internal/blob` | An object store behind one interface (get, put, conditional put, delete, list), with a local directory for development and Vercel Blob, plus the `blob_object` metadata table. S3 and others are further implementations. | Furniture Magic 6.3 |

Bloomprint's `internal/auth` mixes identity (credentials) with accounts (who uses the product).
`plinth` splits them, as Furniture Magic's spec §4 requires: an account refers to an identity by
issuer and subject, so a second provider needs no change to accounts.

**System roles and permissions are `plinth`'s** (decided 2026-10-07). Bloomprint's single
administrator role becomes a role in `rbac` when Bloomprint moves onto `plinth` (§12).

**Blob storage is behind an interface,** so a product chooses Vercel Blob, S3 or another store by
configuration, not code. The provider's credentials are settings, never environment variables.
Bloomprint's `NewVercelFromEnv` doesn't come across.

## 3. Storage

**Each package owns its persistence.** Bloomprint has one `store` package behind one interface.
`plinth` can't: a product would have to implement every package's storage itself. So each
package that stores anything provides:
- a `Store` interface: the operations the package needs, and nothing a product would query
- `mem`: an in-memory implementation, for tests and for running a product without a database
- `pg`: the Postgres implementation, on `pgx/v5`
- a conformance suite in the package's `storetest` subpackage, which runs against both

A product's tests use `mem`. The conformance suites are what make that safe.

**Transactions and attribution.** Every write runs in a transaction that has run
`set_config('app.modified_by', <account uuid>, true)` first, so the shadow log records who made
the change. The transaction helper does both. A `pg` store method takes the transaction from its
context and never opens its own, so a product's write and a `plinth` write commit together.

## 4. Schema and migrations

**`plinth`'s tables live in the product's app schema,** beside the product's own. The product's
foreign keys reference them, most often `account (id)`. Like the product's, `plinth`'s migrations
**never name a schema or a role**.

**Migrations ship in the module.**
- Each package embeds its migrations, and `plinth` exposes them as one ordered set.
- A product's migrate command applies `plinth`'s set **before** its own, with its own version
  table, `plinth_schema_version`. The two version sequences are independent.
- **A product never alters a `plinth` table.** It extends one with its own table keyed by the
  same id (§6).
- **Once released, `plinth`'s migrations are additive only.** The warehouse reads these tables
  under a published contract, so a column is never renamed, retyped or dropped. A new major
  version may break this, and says how in its release notes.
- **Every column carries a `COMMENT`.** That's the warehouse contract, as for the product's
  tables.
- **Every table is created with `shadow()`,** which `plinth`'s first migration installs, unless
  it's registered as exempt (sessions and tokens).

**The table manifest.** For each table, `plinth` declares in Go:
- its **class**: entity (soft-deleted), link (hard-deleted), ephemeral (hard-deleted and
  unlogged), append-only, or reference (reconciled from code)
- its **secret columns** (`password_hash`, `google_subject`, `token_hash`, settings ciphertext)
- whether the runtime role may **delete** from it

A product's grants step reads the manifest, so `plinth`'s tables get exactly the treatment the
product's own get:
- the runtime role's `DELETE` list
- the read-only role's column allowlist, which leaves secret columns out
- the log exclusions

A conformance test checks the manifest against the database's catalogue.

## 5. Database roles

`plinth` names no roles. It relies on the separation the product sets up (Furniture Magic's
data-model §2), and checks it:
- **Migrations** run as a role that has done `SET ROLE` to the owner, so the owner owns every
  object.
- **The runtime role** can't change the schema or write the log schema.
- **The extract role** reads only the log schema and the `<app>_extract` views.
- **`shadowlog`'s boot checks** refuse to start in production, and warn elsewhere, if the
  runtime role:
  - is a superuser, or has `CREATEROLE`, `CREATEDB`, `BYPASSRLS` or `REPLICATION`
  - is a member of `pg_write_all_data`, `pg_read_all_data` or `pg_execute_server_program`
  - owns any table
  - can `SET ROLE` to the owner

The product passes in the role names. They may come from connection strings
(`RuntimeRoleFrom`, as in Bloomprint).

## 6. Accounts: where `plinth` stops

`plinth` owns **what every product's account has**. A product adds what only it needs as its own
table, keyed one-to-one by `account_id`. It never adds a column to `account`.

| `plinth` owns | A product owns |
|---|---|
| `locale` | display preferences (for Furniture Magic: units and precision) |
| `local_identity`, `identity_token`, `session`, `auth_event` | notification choices |
| `account`: id, identity issuer and subject, tier and tier override, active, tombstone, quota time zone, last sign-in, audit columns | business identity (Furniture Magic's `account_studio`) |
| `account_profile`: display name, time zone, locale, email | anything about the product's own objects |
| `tier`, `feature_flag`, `account_flag`, `quota_key`, `tier_quota`, `account_quota`, usage | |
| `app_setting`, the billing tables, the communication log | |
| `system_permission`, `system_role`, `system_role_permission`, `account_system_role` | the permission codes it declares, and project-level roles |
| `blob_object` | which objects it stores, and who may read them |

**This moves columns in Furniture Magic's draft schema.** Its `account` has `units`,
`metric_precision_um`, `imperial_denominator`, `notify_comments` and `notify_signoffs`. They move
to a Furniture Magic table, `account_preference`, when its task 2.2 builds the schema.

**Erasure** (Furniture Magic data-model §10.4) scrubs `plinth`'s personal columns by the same
mechanism. The product registers its own personal columns with it.

## 7. Identifiers

- Keys are UUIDv7 in native `uuid` columns.
- The API shows them as a prefix and Crockford base32, e.g. `acc_01M483M2YGE1CTRQSGT39SE6KV`.
- `plinth` reserves its own prefixes (`acc_` for an account, `idn_` for an identity, `tie_` for a
  tier), and a product registers its own.
- A test refuses a prefix registered twice.
- Secrets are never ids: tokens are stored as SHA-256 hashes, and are never shown as ids.

## 8. Configuration

- **Settings are declared in Go by the package that reads them,** e.g. `email` declares
  `email_provider_key` and `email_from`. A product declares its own beside them.
- **The set is reconciled into `app_setting` at boot.** Every value is encrypted under
  `ENCRYPTION_KEY`, secret or not. The values are re-read every three minutes, so a change needs
  no deploy (Bloomprint's settings-in-the-database design).
- **The environment** holds only what's needed to reach the database and decrypt the settings.
  The product's spec lists those names and freezes the list.
- **A flag is product, a setting is platform.** Anything that varies by account or tier is a
  flag or a quota, never a setting.

## 9. Errors, events and language

- **Errors** are `code.Error` values (package `code`): a stable code and string parameters. The
  product renders them in the reader's locale.
- **Auth events** (sign-in, failed sign-in, password change, token issued) are rows in
  `auth_event`, which is append-only and logged.
- **Email** is the only rendered text.
  - Each email kind has a message id per part (subject, body blocks).
  - Templates are rendered in the recipient's `account_profile.locale`.
  - A product supplies message catalogues for its own kinds, and may override `plinth`'s.
  - English is en-GB.

## 10. Versioning

- `plinth` follows semantic versioning. It stays at `v0` until Furniture Magic launches.
- A product pins an exact version in its `go.mod` and states it in its own spec.
- **A minor version** may add packages, functions, settings, tables and columns. It may not
  change a code's meaning, remove a setting, or break a migration already released.
- **A major version** may break those, with a migration path in its release notes.

## 11. Testing

- `make test` runs the unit tests and the conformance suites against `mem`.
- `make test-db` runs the conformance suites, the manifest check, the role-separation checks
  and the migration checks against Postgres 18. `make db-up` starts it in Docker on port 5435,
  so it can run beside a product's database on 5434.
- **Each test gets its own schema pair** (`<schema>` and `<schema>_log`). That's why there are no
  extensions: an extension is per database, not per schema.
- CI runs both, plus `go vet` and `gofmt`, on every push and pull request.
- **Test first:** a lifted package arrives with its Bloomprint tests passing, and changes are
  driven by a failing test.

## 12. Bloomprint's move onto `plinth`

Bloomprint changes only when it adopts `plinth`. It will then need these changes:
- **Accounts:** its `User` splits into an identity and an account (§2, §6).
- **Roles:** its administrator role becomes an `rbac` role, with its powers as declared
  permissions, and the last-holder guard replaces its own check.
- **Storage:** its single `store` package gives way to each package's own store (§3).
- **Blob storage:** its Vercel Blob credentials move from the environment to settings.

## 13. Open questions

1. **Billing tables.** These are reviewed once the difference between what Bloomprint and
   Furniture Magic need from billing is known. Until then, `billing` is lifted as Bloomprint has
   it.
