# igorski89/listmonk fork: Postgres + SQLite

Fork of knadh/listmonk that adds an SQLite backend next to Postgres
(upstream stopped taking code contributions in Nov 2026). Design and
history: SGNL `instance/docs/PLAN-listmonk-sqlite.md`.

- Storage interfaces: `internal/store` (`QUERIES.md` maps every upstream
  query to a method).
- Parity harness: `tests/parity` (`setup.sh`, then `compare.sh`): installs
  a baseline build and this tree into a local Postgres, runs the same API
  session, diffs normalised responses, database dumps and sent mail.
- Branch `sqlite`, based on upstream tag `v7.0.0-beta.2`; upstream
  releases are merged by tag.

The parity session covers: lists, subscribers (incl. SQL filters,
forbidden-table and write attempts), import, templates, media, campaigns
(incl. a real send to mailpit), tracking, public pages, bounces, users,
roles, password login and sessions (incl. password change logging out
other sessions), a list-scoped API user, maintenance. Run it also with
`VERSION=v7.0.0-beta.2` for the non-final-build startup path.

## Step 1: app on the store interfaces, Postgres only, no behaviour change

Every commit builds, vets and passes `tests/parity/compare.sh`.

- [x] Interfaces drafted (`internal/store`)
- [x] Parity harness; baseline-vs-baseline diff is empty
- [x] `store/postgres`: implementation wrapping upstream `queries/*.sql`
- [x] core + cmd + importer + bounce + auth + manager on the store (parity OK)
- [x] `models.Queries` used only inside `store/postgres`
- [x] Parity also with password sessions, a list-scoped user and the
      beta (non-final) startup path; interface comments match the store
- [x] Model fields to plain Go (decision B: 7 at beta.2), row types in
      `store/postgres/rows.go`; `lib/pq` now only in `store/postgres` and
      `internal/migrations`

## Step 2: conformance suite

`internal/store/storetest`: one suite every backend must pass, written
against the interfaces, asserting values (filters, scopes, sort/paging,
side effects, error mapping, JSON shapes) with Postgres as the reference.
Run on Postgres:

    tests/parity/setup.sh
    LISTMONK_TEST_PG_DSN="host=127.0.0.1 port=55432 user=listmonk password=listmonk dbname=listmonk sslmode=disable" \
      go test ./internal/store/postgres/ -coverpkg=./internal/store/postgres

Skipped without the DSN, so `go test ./...` passes without Docker.

- [x] Suite green on Postgres, 81% statement coverage of `store/postgres`.
      Not covered: running actual migrations (needs an old schema), the
      cached subscriber-count path (upstream's query code never takes it:
      it passes "TRUE", not "", as the condition), transaction-begin
      failures, "role in use" on DeleteListPermission.
- [x] Self-check: breaking a row mapping turns the suite red.
- [x] SQLite store (`internal/store/sqlite`, modernc.org/sqlite, no cgo):
      the whole conformance suite passes (81% coverage).
      Run: `go test ./internal/store/sqlite/ -timeout 120s`. 
### Deviations from upstream behaviour (to review)

None show in the parity session. Known differences on paths it doesn't
exercise:

- Campaign manager: fetching a finished campaign now also loads its stats
  (one more query per finished campaign; same notification mail).
- List roles: create/update of a list role and its per-list permissions is
  one transaction (upstream: two statements, no transaction).
- Updating a user role that doesn't exist: 400 "not found" (upstream: 500
  "sql: no rows in result set").
- Import: any failed batch fails the import (upstream: an insert error
  stopped it, but reported "finished" if it hit the first row of a batch;
  commit errors were logged and the import went on).
- Campaign manager `DeleteSubscriber`: works (upstream passed one argument
  to the two-argument `delete-subscribers`). `RecordBounce` in
  `cmd/manager_store.go` removed: unused, and it ran the wrong statement.
- `--install --idempotent` checks for the `templates` table, not
  `settings` (both are created by the schema).
- Deleting a role that users still have: 400 "can't delete role"
  (upstream: 500 with the database error; it checked for a constraint
  name, `users_role_id_fkey`, that schema.sql doesn't have).
- Subscriptions can be listed by subscriber ID alone (upstream's query
  failed on an empty UUID; upstream only ever called it with a UUID).
- A few error messages on internal-failure paths (failed transaction
  begin, failed lazy list load) are worded like their neighbours.
