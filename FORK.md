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
- [ ] `store/postgres`: implementation wrapping upstream `queries/*.sql`
- [ ] core + cmd + importer + bounce + auth + manager on the store
- [ ] `models.Queries` used only inside `store/postgres`
- [ ] 8 model fields to plain Go (decision B), row types in the store

### Deviations from upstream behaviour (to review)

(none yet)
