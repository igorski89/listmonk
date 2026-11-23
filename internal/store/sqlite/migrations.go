package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
)

// Step is one SQLite schema migration. SQLite databases don't follow
// upstream's version chain: they started from a baseline (schema.sql),
// and upstream kept changing its v7.0.0 migration after that baseline, so
// a version number can't say what a database has. Instead each step has a
// name, recorded in the schema_migrations table when it has run.
//
// Steps are append-only: never rename, reorder or change one that has been
// tagged; add a new one. schema.sql is always the result of all of them,
// so Install records them all as applied. A step must also be safe on a
// database where its change was made some other way (an earlier fixup,
// a hand fix): check, then change.
type Step struct {
	Name string
	Fn   func(ctx context.Context, tx *sqlx.Tx, env store.MigrationEnv) error
}

var steps = []Step{
	{
		// Upstream declares users.list_role_id ON DELETE CASCADE, so deleting
		// a list role deleted its users (knadh/listmonk#3375). Applied
		// before the ledger existed by a schema-checking fixup (tag
		// v7.0.0-beta.2-sqlite.3), hence the check.
		Name: "fork-users-list-role-set-null",
		Fn: func(ctx context.Context, tx *sqlx.Tx, _ store.MigrationEnv) error {
			ok, err := listRoleCascades(ctx, tx)
			if err != nil || !ok {
				return err
			}
			return rebuildUsers(ctx, tx)
		},
	},
}

func listRoleCascades(ctx context.Context, q sqlx.QueryerContext) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT COUNT(*) FROM pragma_foreign_key_list('users')
		WHERE "from" = 'list_role_id' AND on_delete = 'CASCADE'`)
	return n > 0, wrap(err)
}

// rebuildUsers recreates the users table with the schema.sql definition
// (SQLite can't alter a foreign key in place). It runs inside the
// migration transaction, where foreign_keys can't be switched off: that is
// safe only because no table, view or trigger refers to users, which it
// checks first.
func rebuildUsers(ctx context.Context, tx *sqlx.Tx) error {
	var deps []string
	if err := tx.SelectContext(ctx, &deps, `SELECT m.name FROM sqlite_master m
		WHERE m.name <> 'users' AND (
			(m.type IN ('view', 'trigger') AND m.sql LIKE '%users%') OR
			(m.type = 'table' AND EXISTS (SELECT 1 FROM pragma_foreign_key_list(m.name) f WHERE f."table" = 'users')))`); err != nil {
		return wrap(err)
	}
	if len(deps) > 0 {
		return fmt.Errorf("rebuilding users: %v refer to it", deps)
	}

	// The AUTOINCREMENT counter: DROP TABLE deletes it, and without it a
	// deleted top ID would be reused.
	var seq int64
	if err := tx.GetContext(ctx, &seq, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'users'), 0)`); err != nil {
		return wrap(err)
	}

	// The DDL below is fixed, so it must match the table it replaces: a
	// later migration that added a column would otherwise lose it here.
	const cols = `id, username, password_login, password, email, name, avatar, type,
		user_role_id, list_role_id, status, twofa_type, twofa_key, loggedin_at, created_at, updated_at`
	var have []string
	if err := tx.SelectContext(ctx, &have, `SELECT name FROM pragma_table_info('users') ORDER BY cid`); err != nil {
		return wrap(err)
	}
	if want := strings.Join(strings.Fields(strings.ReplaceAll(cols, ",", " ")), ", "); strings.Join(have, ", ") != want {
		return fmt.Errorf("rebuilding users: columns are (%s), expected (%s)", strings.Join(have, ", "), want)
	}
	stmts := []string{
		`CREATE TABLE users_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    username         TEXT NOT NULL UNIQUE,
    password_login   BOOLEAN NOT NULL DEFAULT false,
    password         TEXT NULL,
    email            TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    avatar           TEXT NULL,
    type             TEXT NOT NULL DEFAULT 'user' CHECK (type IN ('user', 'api')),
    user_role_id     INTEGER NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    list_role_id     INTEGER NULL REFERENCES roles(id) ON DELETE SET NULL,
    status           TEXT NOT NULL DEFAULT 'disabled' CHECK (status IN ('enabled', 'disabled')),
    twofa_type       TEXT NOT NULL DEFAULT 'none' CHECK (twofa_type IN ('none', 'totp')),
    twofa_key        TEXT NULL,
    loggedin_at      TIMESTAMP NULL,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
)`,
		`INSERT INTO users_new (` + cols + `) SELECT ` + cols + ` FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_new RENAME TO users`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return wrap(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'users'`, seq); err != nil {
		return wrap(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) SELECT 'users', ? WHERE ? > 0 AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'users')`, seq, seq); err != nil {
		return wrap(err)
	}

	var bad int
	if err := tx.GetContext(ctx, &bad, `SELECT COUNT(*) FROM pragma_foreign_key_check('users')`); err != nil {
		return wrap(err)
	}
	if bad > 0 {
		return fmt.Errorf("rebuilding users: %d rows fail the foreign key check", bad)
	}
	return nil
}
