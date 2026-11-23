# listmonk, igorski89 fork: Postgres + SQLite

This is a fork of [knadh/listmonk](https://github.com/knadh/listmonk), the
self-hosted newsletter and mailing list manager. It adds **SQLite** as a
second database backend, next to Postgres.

The fork has been maintained on its own since November 2026, when upstream
stopped accepting code contributions. It isn't affiliated with the upstream
project. Please report problems with this fork here, not upstream.

## Goals

- **SQLite as a first-class backend.** One database file and no database
  server, for small single-node installs. Postgres stays supported and
  remains the default.
- **Same behaviour as upstream.** Both backends pass one conformance
  suite. A parity harness runs the same API and admin session against
  upstream and against this fork, then compares the responses, the
  database contents and the mail sent. The differences that remain are
  listed in [FORK.md](FORK.md).
- **Safe upgrades.** Each release is tested by upgrading a real database
  from the previous release before its image is published.
- **Fixes we need, when we need them.** Upstream isn't tracked. Its changes
  are merged only when we decide to check.

## Differences from upstream

- **SQLite backend** (`[db] driver = "sqlite"`), built on
  [modernc.org/sqlite](https://modernc.org/sqlite), so no cgo is needed
  and it's still a single static binary. Some behaviour differs by design:
  - search matches substrings (`LIKE`) instead of using full-text search;
  - case-insensitive matching folds ASCII only;
  - dashboard and list statistics are always computed live;
  - raw SQL subscriber filters are written in SQLite SQL and run in a
    sandbox (allowed tables and functions only);
  - schema changes are named migration steps, recorded in a
    `schema_migrations` table, not upstream's version chain.
- **Storage layer.** The app talks to the database through per-domain store
  interfaces (`internal/store`), with one implementation per backend. The
  Postgres store wraps upstream's `queries/*.sql`. `internal/store/QUERIES.md`
  maps every upstream query to the method that replaces it.
- **OIDC auto-login** (`security.oidc.auto_login`). The login page sends
  the user straight to the identity provider. `/admin/login?password`
  still offers password login, as a break-glass route.
- **Bug fixes.** For example, deleting a list role no longer deletes the
  users who have it (upstream
  [#3375](https://github.com/knadh/listmonk/issues/3375)). Existing
  Postgres databases are repaired automatically on upgrade.
- **Releases.** Tags look like `v7.0.0-beta.2-sqlite.5`: the upstream
  version the fork is based on, then a fork release number. Images are
  published to GHCR for linux/amd64 and linux/arm64. The fork publishes no binaries
  and no Docker Hub images.

[FORK.md](FORK.md) has the details: the design, test results, every known
difference, backups, migrations and the release process.

## Running it

### Docker, SQLite

```shell
docker run -d --name listmonk -p 9000:9000 -v listmonk-data:/listmonk/data \
  -e LISTMONK_app__address=0.0.0.0:9000 \
  -e LISTMONK_db__driver=sqlite \
  -e LISTMONK_db__path=/listmonk/data/listmonk.db \
  -e LISTMONK_ADMIN_USER=admin -e LISTMONK_ADMIN_PASSWORD=change-me-please \
  ghcr.io/igorski89/listmonk:v7.0.0-beta.2-sqlite.5 \
  sh -c './listmonk --install --idempotent --yes && exec ./listmonk'
```

Then visit `http://localhost:9000`. Pin a tag from the
[releases](https://github.com/igorski89/listmonk/releases). Releases are
pre-releases (`-beta`/`-rc`), and these apply pending migrations when they
start, so upgrading means switching the tag and restarting.

**Backups.** Copying `listmonk.db` alone while the app runs is not a safe
backup, because the database runs in WAL mode. Use
`sqlite3 listmonk.db "VACUUM INTO 'backup.db'"`, or pause the container
and copy `listmonk.db` together with `listmonk.db-wal`. See FORK.md.

### Docker, Postgres

Use the same image, with the `LISTMONK_db__*` settings for your Postgres
server (or `[db]` in `config.toml`). The `docker-compose.yml` in this repo
is upstream's: replace its `image:` with the fork's image.

### From source

Requires Go and [Bun](https://bun.sh). `make dist` builds a `./listmonk`
binary with the frontend and static files packed into it. Run
`./listmonk --new-config`, set `[db]` in `config.toml`, then
`./listmonk --install`.

Most of upstream's documentation still applies: configuration,
features and the API ([listmonk.app/docs](https://listmonk.app/docs)).

## Development

- Tests: `go test ./...` runs the SQLite tests. Set
  `LISTMONK_TEST_PG_DSN` to also run the Postgres ones, against a database
  they may wipe.
- Parity harness: `tests/parity` (`setup.sh`, then `compare.sh`).
- CI: every push and pull request runs `.github/workflows/fork-test.yml`.
  Pushing a release tag runs `.github/workflows/fork-release.yml`.

## License

listmonk is licensed under the AGPL v3 license, and so is this fork.
Original work © Kailash Nadh and the listmonk contributors.
