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
| `env` | `internal/env` | The `.env` loader. It never overrides the environment, reads values literally, and sets only the names the product declares (§8) | 1.3 |
| `settings` | `internal/settings` and `internal/bootstrap` | Declared, encrypted settings rows, reconciled at boot, a three-minute snapshot, bindings, and the write path. The `/admin/settings` handler comes with `rbac`, which guards it (§8) | 1.3 |
| `alert` | `internal/alert` | Errors reported from the log handler, rate-limited and redacted. The product names itself in the subject. The mail sink comes with `email` | 1.3 |
| `actor` | (new) | Who is acting, carried on the context, for attribution (§3) | 1.3 |
| `identity` | `internal/auth`, split | What a provider proves, and the local provider: passwords, reset and verification links, and Google sign-in (§12) | 1.2 |
| `account` | `internal/auth`, split | Accounts, their profile cache and their sessions, keyed to an identity by (issuer, subject) (§12) | 1.2 |
| `ids` | `internal/ids`, and Furniture Magic's `engine/ident` | UUIDv7, prefixed Crockford ids, and secret tokens (§7) | 1.2 |
| `flags` | `internal/flags` | Tiers, and feature flags by tier with per-account overrides (§13) | 1.4 |
| `quotas` | `internal/quotas` | Limits by tier, inherited up the ladder, with per-account overrides that can expire (§13) | 1.4 |
| `usage` | (new: Bloomprint's is analytics) | The quota day and the check against a limit. It decides, and the product counts (§13) | 1.4 |
| `billing` | `internal/billing` | Tiers, prices, subscriptions and the stub provider | Furniture Magic 8.3 |
| `email` | `internal/email` | Kinds by category, Postmark, the communication log, and rendering from message catalogues in the recipient's locale (§9) | 1.5 |
| `shadowlog` | spike 0.1 | The boot checks. The `shadow()` helper, the log trigger and the exclusion registry are SQL in `migrations` (§4, §5) | 1.5 |
| `db` | `internal/db` | Opening the pool, the transaction helper that sets the actor, the forward-only migration runner, and the table manifest and its grants (§3, §4) | 1.5 |
| `migrations` | (new) | `plinth`'s migration stream and table manifest (§4) | 1.5 |
| `code` | (new) | Error values: a stable code and string parameters (§9) | 1.1 |
| `pgtest`, `fixture` | `internal/store/storetest` | Per-schema test databases, and the rows a conformance suite needs from other packages (§11) | 1.5 |
| `dataapi` | the `data-api` repository, rebuilt | Extraction of the shadow logs by `(txid, log_id)` cursor, as the extract role, and its HTTP handler (§14) | 1.6 |
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

**Postgres stores** (`<package>/pg`) use two helpers from `db`:
- **`db.Run(ctx, pool, fn)`** runs a write in a transaction, or joins the one `ctx` already
  carries, so a product's write and a `plinth` write commit together.
- **`db.Q(ctx, pool)`** reads through that transaction if there is one.

They map the constraint names a write can violate to their package's codes.

**Cross-package foreign keys.** A conformance suite that needs another package's rows (an
account, a tier) asks a `fixture.Source` for them: `fixture.Minted` mints bare ids for memory,
and `pgtest.Fixtures` inserts real rows, so the foreign keys are exercised too.

**Transactions and attribution.** Who is acting is carried on the context (`actor.With`). Every
write runs in a transaction that has run `set_config('app.modified_by', <account uuid>, true)`
first, from `actor.From`, so the shadow log records who made the change. An in-memory store keeps
the same attribution itself. A write with no actor is honestly authorless: seeding, or a system
job. The transaction helper does both. A `pg` store method takes the transaction from its
context and never opens its own, so a product's write and a `plinth` write commit together.

## 4. Schema and migrations

**`plinth`'s tables live in the product's app schema,** beside the product's own. The product's
foreign keys reference them, most often `account (id)`. Like the product's, `plinth`'s migrations
**never name a schema or a role**.

**Migrations ship in the module,** as one stream: `migrations.Plinth`, embedded SQL files named
`NNNN_description.sql`, with the version table `plinth_schema_version`.
- A product applies `plinth`'s stream **before** its own, with the same runner and its own
  version table. The two version sequences are independent.
- **A product never alters a `plinth` table.** It extends one with its own table keyed by the
  same id (§6).
- **Once released, `plinth`'s migrations are additive only.** The warehouse reads these tables
  under a published contract, so a column is never renamed, retyped or dropped. A new major
  version may break this, and says how in its release notes.
- **Every column carries a `COMMENT`.** That's the warehouse contract, as for the product's
  tables. A test checks every column of every table and log twin.
- **Every constraint is named** `<table>_<what>_<pk|fk|uq|ck>`, because stores map constraint
  names to codes. A test checks every one.

**The runner (`db.Migrate`)** is forward-only. There are no down migrations, and a `.down` file is
refused, because a down that drops a log table destroys audit history.
- It runs as the migration login, after `SET ROLE <owner>`, so the owner owns every object.
- Each file runs in its own transaction, under one advisory lock, so two deploys can't migrate
  at once.
- **It refuses** a file whose checksum differs from the one recorded when it was applied
  (`db.migration_changed`: released migrations are never edited). It also refuses a database
  that has applied a version this code doesn't have (`db.migration_unknown`: the code is older
  than the database).

**The shadow log (`0001_shadow`)** is spike 0.1's, schema- and role-free:
- `log_exclusion` lives in the log schema, and `exclude_from_log(table, column, reason)`
  registers a column there.
- The statement-level `shadow_log_stmt()` trigger is `SECURITY DEFINER` and owned by the owner.
  It writes one `INSERT … SELECT` per statement, and attributes it from `app.modified_by`.
- **`shadow(t, partitioned, guard, noop)`** is called after a table's `CREATE` and `COMMENT`s:
  - It creates the log twin with its `(txid, log_id)` cursor index, copying the table's column
    comments.
  - It adds `_00_pk_immutable`, an optional `_00_guard` (a product's frozen-content guard),
    optional `_05_noop`, and `_10_touch` (setting `updated_by` from the actor where the column
    exists).
  - It adds the three `_20_log` triggers.
- A base column that is neither in the log twin nor excluded fails the next write, loudly.

**The table manifest** (`db.Table`; `plinth`'s is `migrations.Tables`) declares for each table:
- **its class:**
  - `entity`: soft-deleted
  - `reference`: reconciled or configured, never deleted
  - `append_only`: only ever gains rows (a save, an id ever issued), logged, never deleted
  - `counter`: a hot row updated in place whose history is kept elsewhere (a head's revision),
    not logged and never deleted
  - `link`: hard-deleted, and logged
  - `ephemeral`: hard-deleted, and not logged
  - `record`: a record with its own retention (the communication log), hard-deleted when it
    expires, and not logged, because the log would copy its personal data
  - `internal`: a version table, nobody's but the owner's
- **its secret columns,** which no read-only or extract role reads and the log never keeps
- **`NoExtract`,** which withholds its log twin from the extract role. Settings use it: their
  ciphertext is kept in the log for history, but the warehouse has no use for it.

**`db.Grant(roles, tables)`** applies the manifest, `plinth`'s and the product's together, as the
owner on every deploy:
- The runtime role gets `SELECT, INSERT, UPDATE` on every non-internal table (only `SELECT,
  INSERT` on an append-only one), `DELETE` only on link, ephemeral and record ones, and `SELECT`
  on the log.
- The extract role gets the log only.
- The read-only role gets a column allowlist without secrets, and the log.

`db.CheckManifest` compares the manifest with the catalogue: unlisted or missing tables, log
twins that shouldn't or should exist, and secrets in a log.

## 5. Database roles

`plinth` names no roles. It relies on the separation the product sets up (Furniture Magic's
data-model §2), and checks it:
- **Migrations** run as a role that has done `SET ROLE` to the owner, so the owner owns every
  object.
- **The runtime role** can't change the schema or write the log schema.
- **The extract role** reads only the log schema and the `<app>_extract` views.
- **`shadowlog.Check`** is the boot check. A product refuses to start in production, and warns
  elsewhere, if the runtime role (`shadowlog.unsafe_role`):
  - is a superuser, or has `CREATEROLE`, `CREATEDB`, `BYPASSRLS` or `REPLICATION`
  - is a member of `pg_write_all_data`, `pg_read_all_data` or `pg_execute_server_program`
  - owns any table
  - can `SET ROLE` to the owner

The product passes in the role names. They may come from connection strings
(`RuntimeRoleFrom`, as in Bloomprint). A role-separation test logs in as each role and proves the
runtime role:
- can't write, rewrite or delete the log
- can't edit the exclusions
- can't delete an entity
- can't truncate, create a table, disable a trigger, read the version table, or become the owner

It also proves the extract role can't see the app schema, and the read-only role can't read a
secret column.

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

Package `ids`:
- Keys are UUIDv7 in native `uuid` columns. `ids.New` mints one from the clock and
  `crypto/rand`.
- The API shows them as a prefix and Crockford base32, e.g. `acc_01M483M2YGE1CTRQSGT39SE6KV`:
  - 26 upper-case characters, the first no higher than `7`
  - only the canonical form parses, so no two strings name one id
  - a wrong prefix is refused with `id_kind`, and anything else malformed with `id_syntax`
- **Prefixes are registered.** `plinth` registers its own (`acc` for an account, `idn` for a
  local identity, `tie` for a tier), and a product registers its own at start-up.
  `ids.Register` panics on a prefix registered twice, or one that isn't 2–4 lower-case letters.
- **Secrets are never ids.** `ids.Token` is 32 random bytes, base64url without padding. Only
  its SHA-256 (`ids.HashToken`, 32 bytes) is stored, so a database read yields nothing that
  signs anyone in.

## 8. Configuration

**The environment** holds only what's needed to reach the database and decrypt the settings.
- The product's spec lists those names and freezes the list.
- `env.Load(names...)` sets only the declared names from `.env`, never overriding the real
  environment, and reports any other name in the file without setting it.
- The encryption key's conventional name is `ENCRYPTION_KEY` (`settings.EnvEncryptionKey`). It's
  required everywhere, with no default.

**Settings are declared in Go by the package that reads them,** e.g. `identity` declares the
Google credentials and `alert` the operations address and daily ceiling.
- A product builds one `settings.Registry` at start-up from `settings.Core` (`app_url`,
  `api_base_url`), the declarations of the packages it uses, and its own.
- A key declared twice, a malformed key, or a default its own kind refuses panics.
- **A declaration has no prose.** Its name, description and consequence are message ids
  (`settings.<key>.name`), and every value or coherence problem is a code.
- **Coherence rules** are registered with the registry by the package or product that owns the
  settings. A rule needing a fact from outside the snapshot closes over whatever supplies it.
  - A fatal problem refuses a write, and a stored configuration at boot.
  - An advisory problem is a banner.
  - The guard sits on the switch, not the pieces: credentials are saved freely in any order, and
    turning the feature on is what's refused until they're there.

**Storage and encryption:**
- The set is reconciled into `app_setting` at boot. This is insert-only, so a default applies
  once and an administrator's value outranks it on every later deploy.
- Every value is encrypted with AES-256-GCM under the encryption key, secret or not, with a fresh
  nonce per write. The store holds ciphertext only.
- A sensitive value is never shown back, only whether it's set.

**`settings.Live`** is the configuration in use: an immutable snapshot behind an atomic pointer.
- **Loading at boot is fatal on any failure:** an unreadable store, an undecryptable row, or an
  incoherent configuration.
- **A refresh every three minutes** keeps the last good snapshot on failure, and rejects an
  incoherent one whole.
- **Bindings** rebuild a dependency, such as the mailer or the Google client, only when the
  settings it reads change. A failed build keeps the previous one.
- **`Set` is the write path:** parse, validate the transition, encrypt, store, install.
- **Recovery from a lost key:** `Unreadable` reports the rows that won't decrypt, and
  `ResetUnreadable` rewrites them at their defaults under the current key.

**The `/admin/settings` handler** arrives with `rbac`, because it's guarded by a permission.

**A flag is product, a setting is platform.** Anything that varies by account or tier is a flag
or a quota, never a setting.

## 9. Errors, events and language

- **Errors** are `code.Error` values (package `code`): a stable code and string parameters. The
  product renders them in the reader's locale.
- **Auth events** (sign-in, failed sign-in, password change, token issued) are rows in
  `auth_event`, which is append-only and logged.
- **Operator alerts** (`alert`) are plain English to the deployment's operators, not users. The
  product names itself in the subject line.
- **Email** is the only rendered text (`email`).
  - **Kinds** are declared into a registry, each with a category: regulatory (kept six years),
    operational and marketing (400 days), or internal (90 days). A kind may also be marked as
    carrying a credential. `plinth` declares `password_reset`, `verify_address`,
    `address_changed` and `operator_report`.
  - **Message catalogues** are the flat JSON files (`<locale>.json`) a product's web app reads
    too. `plinth` ships en-GB text for its kinds, and a product loads its own over them, so it
    may override any. A kind renders from `email.<kind>.subject` and `email.<kind>.body`, whose
    blank lines separate paragraphs, in the recipient's locale, falling back to en-GB.
  - **HTML** escapes every parameter. A `link` parameter must be an absolute http or https URL,
    and becomes a link.
  - `Catalogue.Missing(locale)` lists what a translation lacks, for the build check.
  - **`Logged` records every message** in `communication_log` before sending it, then how the
    send ended. It withholds the body of a kind carrying a credential. Recording never stops a
    send. A marketing kind must go on the broadcast stream, and nothing else may.
  - **Postmark** is configured by the declared settings `email_provider_key`, `email_from` and
    `email_broadcast_stream`. With either of the first two missing, the sender is `NoOp`, which
    `Discards` reports so a product can log links instead. Header injection in an address or
    subject is refused.
  - **Pruning** goes one category at a time, never younger than 90 days.

## 10. Versioning

- `plinth` follows semantic versioning. It stays at `v0` until Furniture Magic launches.
- A product pins an exact version in its `go.mod` and states it in its own spec.
- **A minor version** may add packages, functions, settings, tables and columns. It may not
  change a code's meaning, remove a setting, or break a migration already released.
- **A major version** may break those, with a migration path in its release notes.

## 11. Testing

- `make test` runs the unit tests and the conformance suites against the in-memory stores.
- `make test-db` runs everything against Postgres 18, including the Postgres stores' conformance
  suites, the migrations, the comment and naming checks, the manifest check, and role separation.
  - `make db-up` starts Postgres 18 in Docker on port 5435, so it can run beside a product's
    database on 5434.
  - `make test-db` sets `PLINTH_TEST_DATABASE_URL`. Without it, Postgres tests skip, so
    `make test` runs anywhere.
- **Each test gets its own schema pair** (`pgtest.New`: `t_<random>` and `t_<random>_log`), with
  `plinth`'s migrations applied. That's why there are no extensions: an extension is per
  database, not per schema.
- **One conformance suite per store,** run against both `mem` and `pg`. A behaviour the two
  disagree on is a suite gap, closed by adding the case.
- CI runs `make lint` and `make test`, and a Postgres 18 service job runs `make test-db`, on
  every push and pull request.
- **Test first:** a lifted package arrives with its Bloomprint tests passing, and changes are
  driven by a failing test.

## 12. Identity and accounts

Bloomprint's `auth` package does both jobs, so it's split in two. Its rules come across
unchanged, and so do its tests.

**`identity` proves who someone is.** It knows nothing about accounts or sessions.
- **An `Identity`** is what a provider proves: issuer, subject, email, whether the email is
  verified, and a name if the provider has one.
- **The local provider** (issuer `local`, subject the `idn_` id) holds `local_identity` and
  `identity_token`. It provides:
  - sign-up
  - checking a password
  - changing a password, which needs the current one
  - changing the email address, which needs the password, and unverifies the account
  - reset and verification links
  - Google sign-in (§12.1)
- **Passwords:**
  - Argon2id, at 64 MiB and three passes, in the PHC string format.
  - At most four hashes run at once, and callers queue for a slot.
  - The only rule is a length of at least 8.
  - An unknown address, an account with no password and a wrong password take the same time and
    return the same code. An inactive account returns that code too, so sign-in can't be used to
    find out which addresses are registered.
- **Tokens** are single-use and hashed, with a purpose (`reset`, `verify`):
  - A reset lasts an hour, and a verification link 48 hours.
  - A verification token's hash includes the address it was sent to, so changing the address
    kills every outstanding link without deleting anything.
  - The purpose is also stored and checked, so neither kind can be spent as the other.
  - A reset link names the address it was sent to, and fails if the identity has moved since.
  - A token is consumed only after the change it authorises has been made.
- **`identity` depends on accounts through one interface** that it declares and `account`
  implements:
  - `Active(issuer, subject)`
  - `Revoke(issuer, subject)`, which ends every session of the matching account

  A password change, a reset, and Google combining with an unverified account all call
  `Revoke`.

**`account` admits an identity.**
- **`Admit(identity, profile defaults)`:**
  - finds the account by (issuer, subject), or creates it with its profile
  - refuses an inactive account with the wrong-password code
  - refreshes the profile's cached email
  - stamps the sign-in
  - issues a session
- **Sessions** last 30 days and slide. Only the token's hash is stored. The expiry is set back to
  a full 30 days once a day or more has passed since it was last set, so a session in use keeps
  sliding, and `last_seen_at` is touched at most once a minute.
- **`Authenticate(token)`** returns the account, and refuses an unknown, expired or inactive
  one.
- **`Deactivate`** ends every session. **`Reactivate`** lets the account sign in again.
- **The data model's `account_identity_ct`** (a local subject must name a live identity) is not
  built. The subject is the `idn_` form, which SQL can't parse cheaply, and `Admit` only ever
  creates an account from an identity the provider has just proved.
- **A crash between steps heals.** If a local sign-up creates the identity but not the account,
  the next sign-in creates the account. Concurrent first sign-ins create one account.
- **A new profile** takes the provider's name, or the address's local part, as its display name.
  Its time zone (default `Europe/London`) must load in Go, and its locale (default `en-GB`) must
  match `locale`'s check.

**The codes** are the contract.
- `identity.`:
  - `invalid_credentials`
  - `invalid_email`
  - `email_taken`
  - `google_linked` (a Google account already linked to another identity)
  - `weak_password` (with `min`)
  - `invalid_token`
  - `already_verified`
  - `no_password`
  - `google_exchange`
  - `google_unverified_email`
  - `google_unconfigured` (with `missing`)
  - `invalid_hash` (a stored hash that doesn't parse, or names parameters outside the bounds this
    package accepts: corruption, not a wrong password, and never a panic)
  - `hash_unavailable` (no salt could be read from the entropy source)
  - `not_found`
- `account.`:
  - `unauthenticated` (a session token that is empty, unknown or expired, or whose account is
    inactive)
  - `identity_taken`
  - `identity_invalid`
  - `time_zone_invalid`
  - `locale_invalid`
  - `not_found`
- An inactive account signing in gets `identity.invalid_credentials`, the same code as a wrong
  password.

**Tiers and the quota day.** An account is on a tier, and support may put it on another
instead (`tier_override_id`). `EffectiveTier` is the one flags and quotas resolve on. A new
account starts on the tier the product configures (`WithDefaultTier`). Its quota time zone is
copied from the profile at creation, and only support changes it. Deletion events and erasure
follow in Furniture Magic's 8.5.

### 12.1 Google sign-in

This is Bloomprint's design, unchanged:
- **The flow** is OAuth with PKCE, a nonce and state.
- **The ID token** is read from the token response over TLS.
- **The claims** are checked: issuer, audience, expiry with two minutes of skew, the nonce, and
  `email_verified` in either its boolean or its string form.

Then, in order:
1. **A linked subject signs in,** whatever its address now is.
2. **A local identity holding the address,** including the `gmail.com`/`googlemail.com` alias,
   is combined:
   - The link is written first.
   - If the address was unverified, Google's proof outranks it: the password is removed and the
     account's sessions are revoked.
   - If that is interrupted after the link, the next sign-in finishes it: a linked, unverified
     identity holding the address Google proves is stripped then. One that moved to a new,
     unverified address keeps its password, because Google hasn't proved that address.
   - An inactive account is refused before anything is written.
3. **Otherwise** a verified identity with no password is created.

An unverified Google address is refused.

## 13. Tiers, flags, quotas and usage

**One mechanism**, as in Bloomprint: ordered tiers, flags with a minimum tier, per-account
overrides, and quotas per tier.
- **Tiers are configuration:** nothing in the source names one.
- **A disabled tier** can't be chosen, but still holds its accounts and still passes its limits
  up the ladder.

**Flags (`flags`).**
- **Declarations** are registered as settings' are: `Default`, `Operational` (a switch for
  operators, not sold), and `Public` (described on a pricing page through message ids).
- **State** is data: enabled, and an optional minimum tier by id.
- **Resolution, in order:**
  - an account override wins outright, in either direction
  - a disabled or retired flag is off
  - with no minimum, it's on
  - otherwise it's on at or above the minimum
- **Failing closed:** no tier, a tier the ladder doesn't hold, or a minimum the ladder doesn't
  hold all resolve off.
- **Unreconciled and stale flags:** a declared flag with no stored state is off. A stored flag
  the source no longer declares is retired, never deleted.

**Quotas (`quotas`).**
- **A declaration** has a unit (`count`, `tokens`), a default (nil for unlimited) and a minimum.
- **A limit resolves to:**
  1. a live per-account override (with reason, grantor, grant time, and optional expiry)
  2. the account's tier, or the nearest tier below it with a row (never one above). An explicit
     "unlimited" row beats a number inherited from below.
  3. the declared default
  4. unlimited
- **An unknown or missing tier** resolves as the lowest enabled tier.
- **Downgrades take nothing away:** going over a limit locks and deletes nothing, and only
  making another is refused.

**Usage (`usage`)** decides and never counts. What has been used is the product's to sum.
- **`Day`** is the quota day: midnight to midnight in the quota time zone, 23 or 25 hours long
  when the clocks change.
- **`Check`** refuses at the limit with `quota_exceeded` (with `quota`, `limit` and
  `resets_at`).
- **`Warning(0.8)`** is the 80% bar.
- **Reservation** (Furniture Magic's assistant turns: an advisory lock, then a sum including
  running turns' reservations) is the product's transaction, built on these.

## 14. The data API

The warehouse reads the shadow logs through `dataapi`, connected as the extract role, which can
see the log schema and nothing else. It's folded in from the standalone `data-api` and rebuilt on
Furniture Magic data-model §10.3.
- **The cursor is `(txid, log_id)`,** read only below `pg_snapshot_xmin(pg_current_snapshot())`.
  A long transaction that commits after a shorter one is never skipped: nothing at or above an
  open transaction's txid is returned until it commits. A time window or a sequence can't promise
  that. A test opens a long transaction to prove it.
- **`Tables`** lists the logs the extract role may read, with their table comments and stored
  cursors. A table whose log is withheld (`NoExtract`) is invisible.
- **`Window`** returns up to 10,000 rows past a cursor, each as Postgres's `row_to_json` (so
  numbers, uuids and times keep their types), with the next cursor.
- **`Ack` and `Reset`:** the consumer acknowledges a cursor once it has kept the page, so a crash
  re-reads rather than loses. Acknowledging is forward-only, and `Reset` forgets.
  - Cursors live in `<app>_log.extract_cursor`, which only the extract role writes.
  - Retention reads them, so a row not yet extracted is never dropped.
- **The extractor is given the app schema's name,** as configuration. The extract role can't use
  the app schema, so it can't find it from the connection.
- **`Handler`** serves `GET /extract`, `GET /extract/{table}`, `POST /extract/{table}/ack` and
  `POST /extract/{table}/reset`. The product mounts it behind its own authentication. Errors are
  JSON codes.
- **Not yet:**
  - **The current-state snapshot** reads the product's `<app>_extract` views (Furniture Magic
    task 8.4), and arrives with them.
  - **An execution history** of extractions is a follow-up.
  - **The standalone `data-api`** serves another product's schema and is left as it is.

## 15. Bloomprint's move onto `plinth`

Bloomprint changes only when it adopts `plinth`. It will then need these changes:
- **Accounts:** its `User` splits into an identity and an account (§2, §6).
- **Roles:** its administrator role becomes an `rbac` role, with its powers as declared
  permissions, and the last-holder guard replaces its own check.
- **Storage:** its single `store` package gives way to each package's own store (§3).
- **Blob storage:** its Vercel Blob credentials move from the environment to settings.

## 16. Open questions

1. **Billing tables.** These are reviewed once the difference between what Bloomprint and
   Furniture Magic need from billing is known. Until then, `billing` is lifted as Bloomprint has
   it.
