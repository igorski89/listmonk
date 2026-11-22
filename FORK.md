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

## Step 1: app on the store interfaces, Postgres only, no behaviour change

Every commit builds, vets and passes `tests/parity/compare.sh`.

- [x] Interfaces drafted (`internal/store`)
- [x] Parity harness; baseline-vs-baseline diff is empty
- [x] `store/postgres`: implementation wrapping upstream `queries/*.sql`
- [x] core + cmd + importer + bounce + auth + manager on the store (parity OK)
- [x] `models.Queries` used only inside `store/postgres`
- [ ] 8 model fields to plain Go (decision B), row types in the store

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
- A few error messages on internal-failure paths (failed transaction
  begin, failed lazy list load) are worded like their neighbours.
