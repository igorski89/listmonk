package sqlite

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// fixup is a fork-only schema repair. Unlike a Migration it isn't tied to
// an upstream version: needed inspects the schema, so a fixup runs once
// wherever it applies (any recorded version, beta builds that don't record
// versions) and is a no-op after, or if upstream ships the same change.
type fixup struct {
	Name   string
	needed func(ctx context.Context, q sqlx.QueryerContext) (bool, error)
	apply  func(ctx context.Context, tx *sqlx.Tx) error
}

var fixups = []fixup{
	{
		// Upstream declares users.list_role_id ON DELETE CASCADE, so deleting
		// a list role deleted its users (knadh/listmonk#3375).
		Name:   "fork: users.list_role_id ON DELETE SET NULL",
		needed: listRoleCascades,
		apply:  rebuildUsers,
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

	const cols = `id, username, password_login, password, email, name, avatar, type,
		user_role_id, list_role_id, status, twofa_type, twofa_key, loggedin_at, created_at, updated_at`
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

// pendingFixups returns the fixups the schema needs.
func pendingFixups(ctx context.Context, q sqlx.QueryerContext) ([]fixup, error) {
	var out []fixup
	for _, f := range fixups {
		ok, err := f.needed(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("checking %q: %w", f.Name, err)
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}
