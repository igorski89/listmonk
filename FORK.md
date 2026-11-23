# igorski89/listmonk fork: Postgres + SQLite

Fork of knadh/listmonk that adds an SQLite backend next to Postgres
(upstream stopped taking code contributions in Nov 2026). Design and
history: SGNL `instance/docs/PLAN-listmonk-sqlite.md`.

- Storage interfaces: `internal/store` (`QUERIES.md` maps every upstream
  query to a method).
- Parity harness: `tests/parity` (`setup.sh`, then `compare.sh`): installs
  a baseline build and this tree into a local Postgres, runs the same API
  session, diffs normalised responses, database dumps and sent mail.
- Branch `master` (also pushed as `sqlite`, its old name), based on
  upstream tag `v7.0.0-beta.2`, with upstream master merged at `6242fd85`
  (2026-11-23, for its security fixes; see "Upstream merges"). Upstream
  isn't tracked: changes are merged only when someone asks for a check.
- CI: `.github/workflows/fork-test.yml` (gofmt, vet, `go test` on SQLite
  and Postgres 18, frontend build) on every push and PR. Upstream's
  workflows are removed.

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
- Migrations: SQLite doesn't use upstream's version chain. Its schema
  started from a baseline (`schema.sql`, upstream v7.0.0-beta.2) and
  upstream kept changing its v7.0.0 migration after that, so a version
  can't say what a database has. Instead `internal/store/sqlite/
  migrations.go` has named steps, append-only, and the table
  `schema_migrations` records the ones applied (Install records all:
  `schema.sql` is their result). Each upstream schema or settings change
  needs a new step, and the same change in `schema.sql`. Steps are recorded
  on beta builds too, and check before changing, so they're safe on a
  database that got the change another way. The `migrations` setting still
  holds the version at install, for the About page.

### Features added in the fork

- OIDC auto-login: `security.oidc.auto_login` (bool, default false; toggle
  "Skip the login page" in Settings -> Security). When OIDC is enabled and
  it's on, `GET /admin/login` does what the login page's OIDC button does
  (fresh nonce cookie, state with `next`, PKCE) and redirects to the
  provider. The form still shows:
  - for `/admin/login?password` (break-glass password login; the
    forgot-password page links there);
  - for `?error=...`;
  - while auto-login is paused in the browser (cookie
    `oidc_autologin_pause`): an hour after a logout, so the provider's
    session doesn't log the user straight back in, and a minute after an
    OIDC login, so a session that doesn't stick can't loop; the OIDC
    button ends the pause;
  - with the error, if the provider can't be reached.
  `next` pointing at the login page or the callback becomes `/admin`. A
  rejected OIDC login renders its error on the callback (no redirect).
  Not changed: an expired 2FA token redirects to `/admin`, which with
  auto-login goes to the provider; password users use `?password`.
  Off, behaviour is upstream's; the only differences in the parity
  session are the new key in the settings and the toggle. No migration:
  a missing key reads as false. Tests: `OIDC=1 OIDC_AUTOLOGIN=1
  tests/parity/run.sh` (self-checking; beta.2 has no such setting),
  passing on both backends.

### Bug fixes in the fork

- Deleting a list role no longer deletes its users: `users.list_role_id`
  is `ON DELETE SET NULL` (upstream: `CASCADE`; knadh/listmonk#3375), so
  the users just lose the list role. New installs get it from
  `schema.sql`; existing databases from a fixup (below). Tests: the
  conformance suite (`testRoles`), `TestFixupListRole` on each backend.
- Postgres fixups (`internal/store/postgres/fixups.go`): fork-only schema
  repairs next to upstream's migration chain, not tied to a version. Each
  checks the schema to see if it's needed, so it runs once, wherever it
  applies, also on beta builds that don't record versions, and becomes a
  no-op if upstream ships the same change. `Pending` lists the needed ones
  by name and `Migrate` runs them after the upstream migrations, each in
  a transaction. The list-role one finds the CASCADE foreign key in
  `pg_constraint` (not by name) and replaces it. On SQLite the same change
  is the step `fork-users-list-role-set-null`: it rebuilds the users
  table (nothing refers to it, which it checks; it refuses if the columns
  aren't the ones it expects), keeping its rows and the AUTOINCREMENT
  counter.

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
  commit errors were logged and the import went on). A batch is 10,000
  queued rows, including existing subscribers skipped as outside the
  user's lists (upstream: 10,000 accepted rows), so after a failure the
  number of rows kept can differ.
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

## Upstream merges

### upstream master `6242fd85` (2026-11-23)

24 commits after v7.0.0-beta.2, several of them security fixes: import
can't update subscribers of lists the user can't manage; signed
subscriber links (`internal.secret_key`, `security.signed_links`);
preference/data management via e-mailed links; Altcha replay; throttle;
bounce webhook body limit; archive status check; verified e-mail change
for users; public subscription API behind its own setting.

Ported to the store layer (Postgres store: upstream's changed queries;
SQLite: by hand):
- `Users.UpdateEmail` (new query `update-user-email`); `UpdateProfile` no
  longer changes the e-mail.
- `Subscribers.Import` honours `ImportOptions.Permitted` and returns the
  skipped e-mails (upstream's new `$9` on `upsert-subscriber`); the
  importer logs and doesn't count them. Upstream's new importer tests run
  on the SQLite store (they used Postgres temp tables and the statements).
- `Subscriptions.ConfirmOptin` changes only unconfirmed subscriptions.
- `Campaign.ListUUIDs` (plain `[]string`, for signed opt-in links) in
  get-campaign, preview, archive and the sending loop.
- The secret key is generated by `Schema.Install` on both backends.
- SQLite `schema.sql` defaults and the step `upstream-v7.0.0-after-beta.2`
  for existing databases: upload settings as objects (existing values
  kept), `upload.max_file_size` (existing kept), `security.signed_links`
  off for existing installs (on for new ones, as upstream),
  `app.enable_public_subscription_api` false, a random
  `internal.secret_key`. Without the key the app now refuses to start.
- Conformance tests for all of the above, on both backends.

Parity baseline is now `6242fd85` (`compare.sh` default). The session
turns on `app.enable_public_subscription_api` (its public sign-up uses it)
and `normalize.py` masks signed-link `exp`/`sig`. Result: Postgres = master
except the auto-login setting; SQLite only the expected differences;
OIDC on SQLite only IDs; auto-login checks pass.

For SGNL (deploy needs igor's go): the step runs on boot; afterwards
signed links are off (as upstream for upgraded installs; turn on in
Settings -> Security), and the public subscription API is off (the
public subscription page isn't affected). Settings written through the
API with the old `upload.filesystem.*` / `upload.s3.*` keys no longer
apply.
