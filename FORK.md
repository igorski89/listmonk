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
other sessions), a list-scoped API user, maintenance, and 23 server-rendered
admin pages (dashboard, lists, campaigns incl. analytics and activity,
templates, subscribers incl. SQL query, bounces, import, media, users,
roles, profile, settings). Run it also with
`VERSION=v7.0.0-beta.2` for the non-final-build startup path.

`OIDC=1` (for both `setup.sh` and `compare.sh`) adds an OIDC session
(`tests/parity/oidc/`) against a local Keycloak 26.6 (realm in
`oidc/realm.json`, browser side scripted with urllib). It turns OIDC on
through the settings API and covers: login of an existing user matched by
e-mail; unknown user with auto-create off (rejected) and on (created
without password, e-mail lowercased, default roles); role mapping from an
array claim (`groups`) and a string claim (`preferred_username`), incl. a
list role; role sync on a later login (Keycloak group removed, back to the
default role, list role cleared) that leaves the password of a
password+OIDC user intact; disabled user and unverified e-mail rejected;
logout with Keycloak's end-session URL and session removal; deleting a
role referenced in the OIDC settings (400). Result (2026-11-23): fork on
Postgres identical to beta.2; fork on SQLite differs only in IDs (the
Postgres sequence gaps above); the users table matches beta.2's. It tests
listmonk against a local Keycloak. SGNL also runs Keycloak 26.x: the
`listmonk` client in `sgnl/keycloak/realm.yaml` is likewise confidential,
PKCE S256, with a `groups` scope. Its listmonk settings
(`sgnl/bin/sgnl-listmonk-setup`) use auto_create_users = false and no role
mapping, a case the session covers. Not tested: SGNL's browser-flow
override (`sgnl-apps`: sgnl_users group + second factor); that happens
in Keycloak, before listmonk sees the callback.

## Status (2026-11-23 morning)

Done overnight, all on branch `sqlite`, each commit green:
store interfaces -> parity harness -> Postgres store -> app on the store
(parity OK vs v7.0.0-beta.2) -> plain-Go models -> conformance suite
(green on Postgres) -> SQLite store (suite green, sandboxed SQL filters)
-> `db.driver` switch -> parity on SQLite (only expected differences).

Not done / next:
- Review of the deviations and SQLite differences below.
- SGNL build: tag `v7.0.0-beta.2-sqlite.1` (74e3c328); the Dockerfile
  lives in the SGNL repo (`docker/listmonk/Dockerfile`), built from the tag.
- Deploy-side changes for SGNL need igor's go; nothing was changed on
  the server. They are:
  - the Kamal config;
  - backup of the SQLite file (see below);
  - removing the Postgres accessory;
  - `bin/sgnl-listmonk-setup`, which writes the settings (root_url, OIDC,
    from_email, SMTP/SES, bounce.ses) by sending Postgres SQL to the
    Postgres container via `docker exec ... psql` (jsonb operators,
    dollar quoting). It needs a SQLite version, or better, a move to the
    settings API (PUT /api/settings; send "" for unchanged secrets);
  - `bin/sgnl-backup-restore-test-listmonk` and `bin/sgnl-breakglass-export`,
    which assume Postgres.
- SQLite migrations after v7.0.0 (none yet); rebase onto the v7.0.0 tag
  when it ships.
- Data move Postgres -> SQLite: not built (SGNL has no data to move).

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
- [x] App runs on SQLite: `[db] driver = "sqlite"`, `path = "listmonk.db"`
      (or `LISTMONK_db__driver=sqlite`); Postgres stays the default.
- [x] Parity on SQLite: `BACKEND=sqlite tests/parity/compare.sh` runs the
      same session on SQLite against the Postgres baseline (API + mail).
      Remaining differences, all expected:
      - error message wording (rejected SQL expressions, unique violations:
        the database's own text);
      - IDs: Postgres sequences skip numbers when an insert conflicts
        (upserts, duplicate names); SQLite doesn't, so later IDs differ;
      - CSV export: `attribs` JSON spacing and key order (Postgres prints
        jsonb's canonical form);
      - subscriber data export: order of the subscriptions (unordered
        upstream);
      - admin pages: the same ID gaps; plus run-to-run noise in both
        backends (clock minutes shown on campaign/list pages, and the
        order of two hidden inputs on the templates page, which comes
        from Go map iteration).

### SQLite at scale

`LISTMONK_TEST_SCALE=100000 go test ./internal/store/sqlite/ -run TestScale -v`
(laptop, 2026-11-23, 100k subscribers on one list):
import 11.0 s; subscriber page, list filter, search, SQL expression,
lists with counts, dashboard counts 46-78 ms each; counting a campaign's
recipients 58 ms; fetching all 100k recipients in batches of 1000 4.4 s;
1000 view registrations 120 ms.

### SQLite differences by design

- Search uses substring matching (`LIKE`), no stemming (Postgres also
  uses full-text search for list, campaign, template and role names).
- Case-insensitive search and e-mail uniqueness fold ASCII only.
- Stats (dashboard, list counts and charts) are always live; there are no
  materialized views and `app.cache_slow_queries` has no effect.
- Raw SQL subscriber filters are SQLite SQL, sandboxed by the authorizer
  (allowed tables and functions only); expressions containing `;` outside
  quotes are rejected.
- Backups: with WAL, copying the `.db` file alone isn't a safe backup.
  Either `sqlite3 listmonk.db "VACUUM INTO 'backup.db'"`, or copy the `.db`
  and `-wal` together while the container is paused (`docker pause`): that
  copy is a crash image, which SQLite recovers (synchronous=NORMAL). Tested
  2026-11-23: copy taken mid-write passes integrity_check and boots.
  Restore both files together, with the container stopped.
- Migrations: SQLite starts from a baseline at upstream v7.0.0; each later
  upstream migration needs an SQLite twin in `internal/store/sqlite`.

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
