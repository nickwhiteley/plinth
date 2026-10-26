# Backlog

**What isn't built yet.** `spec.md` describes what is. Items are removed from this file when they
ship, and their reasoning moves into `spec.md`.

Ids are `PL` and a number, never reused. The first line under each heading is its meta line, as
in Bloomprint's and Furniture Magic's backlogs, so Project Magic reads it the same way. A change that
spans repositories is one epic, declared in the repository that owns it: `Epic: bloomprint:EP1`
joins Bloomprint's.

### PL1 Port what Bloomprint built after the lift
**Status:** todo · **Size:** M · **Epic:** bloomprint:EP1 · **For:** agent.claude.opus

**Bloomprint is moving onto `plinth`** (`bloomprint:BP29`), and it has built these since its
packages were lifted around 2026-10-07. Each has to be in `plinth` first, or the move takes a
working feature away. Furniture Magic gets them too.

Each one keeps `plinth`'s rules: codes rather than prose, migrations that only add, and a
conformance case run against both `mem` and `pg`.

- **`alert`: the fault record** (Bloomprint BP18, `spec.md` §15 there).
  - A fault is kept before it is sent. `fault` and `fault_occurrence` are written synchronously,
    by fingerprint, and the email follows, so a deployment with no operations address still has a
    record.
  - An occurrence carries `request_id`, `account_id` and `route` where it has them, so support can
    find it from the reference on an error page.
  - The recorder's own failure is never logged through `alert.Handler`, or it would report itself
    for ever.
- **`alert`: `Reporter.Flush(ctx)`** (Bloomprint BP28).
  - It waits until every queued send has finished, the context ends, or the reporter is stopped.
  - A product calls it before a response completes, because a serverless instance may be frozen
    once the response has gone, leaving a send half-done.
- **`email`: retries, *send again* and *not needed*** (Bloomprint BP28).
  - **New columns:** `attempts`, `last_attempt_at` and `reply_to`, with `dismissed_by` and
    `dismissed_at`, and a `dismissed` status.
  - **Stuck:** a row is stuck when it has been `queued` for over ten minutes since its latest
    attempt began. Bloomprint's operations screen and Emails screen both count by that one rule,
    so they agree.
  - **Retries:** a `failed` row whose body was kept is retried in place, after 20 minutes, 2 hours
    and 6 hours: four attempts within a day. Running out of attempts is one ERROR.
  - **The claim** compares the attempt count it read, so two instances never both send. It also
    records the provider making this attempt.
  - **Pure rules:** `Stuck`, `RetryDue`, `NextRetry` and `ActionsFor`.
  - **The product drives it.** `plinth` starts no goroutine, so the product calls the retry pass
    from its own clock.
  - **Rows that carry a credential** kept no body, so they can only be dismissed.
  - Bloomprint's table is written through `SECURITY DEFINER` functions. `plinth`'s is a `record`
    table the runtime role may update, so each rule becomes a `WHERE` clause in the `pg` store,
    held by the conformance suite.
- **`billing`: proration and renewal reminders** (Bloomprint `spec.md` §18, NW7). `spec.md` §13b
  lists both as *not yet*.
  - **An upgrade is paid for before it happens.** It is prorated to the rest of the period: the
    new price for what is left, less the unused part of the old.
  - Nothing about the subscription moves until the money lands: not the tier, the plan or the
    pending change.
  - It is a checkout with `kind = 'upgrade'`, reconciled through the same `Reconcile`. The renewal
    date never moves.
  - **Reminders before a renewal** are recorded in a `billing_reminder` table, once each.
- **`settings`: refresh from a request when stale** (Bloomprint BP12). Add
  `Live.RefreshIfStale(ctx)`, which re-reads only when the snapshot is older than
  `RefreshInterval`. A deployed product calls it per request instead of running `Start`'s ticker,
  so an idle database can suspend.

**Spec first:** each part goes into `spec.md` before its code merges, and arrives with
Bloomprint's tests passing. Tag a minor version when all of it has landed, for Bloomprint to pin.
