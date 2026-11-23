package postgres

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
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
		Name: "fork: users.list_role_id ON DELETE SET NULL",
		needed: func(ctx context.Context, q sqlx.QueryerContext) (bool, error) {
			names, err := listRoleCascadeFKs(ctx, q)
			return len(names) > 0, err
		},
		apply: listRoleSetNull,
	},
}

// listRoleCascadeFKs returns the names of the ON DELETE CASCADE foreign
// keys on users.list_role_id (upstream's is users_list_role_id_fkey, but
// the name isn't assumed). None if the table doesn't exist yet.
func listRoleCascadeFKs(ctx context.Context, q sqlx.QueryerContext) ([]string, error) {
	var names []string
	err := sqlx.SelectContext(ctx, q, &names, `SELECT c.conname FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		WHERE c.conrelid = TO_REGCLASS('users') AND c.contype = 'f' AND c.confdeltype = 'c'
		AND a.attname = 'list_role_id' AND CARDINALITY(c.conkey) = 1`)
	return names, wrap(err)
}

func listRoleSetNull(ctx context.Context, tx *sqlx.Tx) error {
	names, err := listRoleCascadeFKs(ctx, tx)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	for _, n := range names {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE users DROP CONSTRAINT `+pq.QuoteIdentifier(n)); err != nil {
			return wrap(err)
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE users ADD CONSTRAINT users_list_role_id_fkey
		FOREIGN KEY (list_role_id) REFERENCES roles(id) ON DELETE SET NULL`)
	return wrap(err)
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

// runFixup applies f in a transaction, checking again inside it.
func (s schema) runFixup(ctx context.Context, f fixup) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	if ok, err := f.needed(ctx, tx); err != nil || !ok {
		return err
	}
	if err := f.apply(ctx, tx); err != nil {
		return err
	}
	return wrap(tx.Commit())
}
