package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
	"golang.org/x/mod/semver"
)

// Migration is one SQLite schema migration: the SQLite version of the
// upstream migration with the same Version.
type Migration struct {
	Version string
	Fn      func(ctx context.Context, tx *sqlx.Tx, env store.MigrationEnv) error
}

// migrations above the baseline (schema.sql is upstream v7.0.0). Each
// later upstream migration needs its SQLite twin here.
var migrations = []Migration{}

type schema struct{ *Store }
type settings struct{ *Store }

func (s schema) Installed(ctx context.Context) (bool, error) {
	var n int
	if err := s.r.GetContext(ctx, &n, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'templates'`); err != nil {
		return false, wrap(err)
	}
	return n > 0, nil
}

func (s schema) Install(ctx context.Context, version string, env store.MigrationEnv) error {
	// Drop existing tables first, like upstream's schema.sql. Foreign keys
	// are switched off for the drop (the writer pool has one connection,
	// so the pragma applies to the statements that follow).
	conn, err := s.w.Connx(ctx)
	if err != nil {
		return wrap(err)
	}
	defer conn.Close()

	var tables []string
	if err := conn.SelectContext(ctx, &tables, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`); err != nil {
		return wrap(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return wrap(err)
	}
	for _, t := range tables {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS "`+t+`"`); err != nil {
			return wrap(err)
		}
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return wrap(err)
	}

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return wrap(err)
	}
	if err := recordVersion(ctx, tx, version); err != nil {
		return err
	}
	return wrap(tx.Commit())
}

// recordVersion appends a version to the `migrations` array in settings.
func recordVersion(ctx context.Context, tx *sqlx.Tx, ver string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('migrations', json_array(?1))
		ON CONFLICT (key) DO UPDATE SET value = json_insert(settings.value, '$[#]', ?1)`, ver)
	return wrap(err)
}

func (s schema) Version(ctx context.Context) (string, error) {
	var v string
	if err := s.r.GetContext(ctx, &v, `SELECT COALESCE((SELECT value ->> '$[#-1]' FROM settings WHERE key = 'migrations'), 'v0.0.0')`); err != nil {
		if ok, _ := s.Installed(ctx); !ok {
			return "v0.0.0", nil
		}
		return v, wrap(err)
	}
	return v, nil
}

func (s schema) pending(ctx context.Context) ([]Migration, error) {
	last, err := s.Version(ctx)
	if err != nil {
		return nil, err
	}
	for i, m := range migrations {
		if semver.Compare(m.Version, last) > 0 {
			return migrations[i:], nil
		}
	}
	return nil, nil
}

func (s schema) Pending(ctx context.Context) ([]string, error) {
	migs, err := s.pending(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(migs))
	for _, m := range migs {
		out = append(out, m.Version)
	}
	fixes, err := pendingFixups(ctx, s.r)
	if err != nil {
		return nil, err
	}
	for _, f := range fixes {
		out = append(out, f.Name)
	}
	return out, nil
}

// Migrate runs the pending migrations, then the fork's pending fixups,
// each in its own transaction (SQLite DDL is transactional, unlike some of
// upstream's Postgres migrations). Fixups aren't recorded: they check the
// schema instead.
func (s schema) Migrate(ctx context.Context, env store.MigrationEnv, record bool) error {
	migs, err := s.pending(ctx)
	if err != nil {
		return err
	}
	fixes, err := pendingFixups(ctx, s.r)
	if err != nil {
		return err
	}
	if len(migs) == 0 && len(fixes) == 0 {
		env.Log.Printf("no upgrades to run. Database is up to date.")
		return nil
	}
	for _, m := range migs {
		env.Log.Printf("running migration %s", m.Version)
		err := s.inTx(ctx, func(tx *sqlx.Tx) error {
			if err := m.Fn(ctx, tx, env); err != nil {
				return err
			}
			if record {
				return recordVersion(ctx, tx, m.Version)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Again: the migrations may have created the table a fixup repairs.
	if fixes, err = pendingFixups(ctx, s.r); err != nil {
		return err
	}
	for _, f := range fixes {
		env.Log.Printf("running %s", f.Name)
		err := s.inTx(ctx, func(tx *sqlx.Tx) error {
			// Checked again: a migration above may have done it.
			if ok, err := f.needed(ctx, tx); err != nil || !ok {
				return err
			}
			return f.apply(ctx, tx)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	env.Log.Printf("upgrade complete")
	return nil
}

func (s schema) Optimize(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return wrap(err)
	}
	_, err := s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return wrap(err)
}

func (s schema) Info(ctx context.Context) (store.DBInfo, error) {
	out := store.DBInfo{Dialect: "sqlite"}
	var size float64
	if err := s.r.QueryRowxContext(ctx, `SELECT 'SQLite ' || sqlite_version(),
		ROUND((SELECT page_count FROM pragma_page_count()) * (SELECT page_size FROM pragma_page_size()) / 1048576.0)`).Scan(&out.Version, &size); err != nil {
		return out, wrap(err)
	}
	out.SizeMB = size
	return out, nil
}

// Settings.

func (s settings) All(ctx context.Context) (json.RawMessage, error) {
	var b []byte
	if err := s.r.GetContext(ctx, &b, `SELECT CAST(json_group_object(key, json(value)) AS BLOB) FROM (SELECT key, value FROM settings ORDER BY key)`); err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(b), nil
}

// Update writes the keys of a JSON object that already exist (as upstream:
// unknown keys are ignored).
func (s settings) Update(ctx context.Context, v json.RawMessage) error {
	var kv map[string]json.RawMessage
	if err := json.Unmarshal(v, &kv); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		for k, val := range kv {
			if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = json(?2) WHERE key = ?1`, k, string(val)); err != nil {
				return wrap(err)
			}
		}
		return nil
	})
}

func (s settings) Set(ctx context.Context, key string, value json.RawMessage) error {
	_, err := s.w.ExecContext(ctx, `UPDATE settings SET value = json(?2), updated_at = ?3 WHERE key = ?1`, key, string(value), now())
	return wrap(err)
}
