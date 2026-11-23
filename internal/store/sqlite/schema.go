package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
)

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
	if err := setSecretKey(ctx, tx); err != nil {
		return err
	}
	if err := recordVersion(ctx, tx, version); err != nil {
		return err
	}
	// schema.sql is the result of every step.
	for _, st := range steps {
		if err := recordStep(ctx, tx, st.Name); err != nil {
			return err
		}
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

// setSecretKey sets internal.secret_key (URL signing) to 32 random bytes,
// hex, if it is empty or missing.
func setSecretKey(ctx context.Context, tx *sqlx.Tx) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	key, _ := json.Marshal(hex.EncodeToString(b))
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('internal.secret_key', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value WHERE settings.value IN ('""', 'null', '')`, string(key))
	return wrap(err)
}

// ledgerSQL creates the step ledger on databases installed before it
// existed (it is in schema.sql too).
const ledgerSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    name        TEXT NOT NULL PRIMARY KEY,
    applied_at  TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
)`

func recordStep(ctx context.Context, tx *sqlx.Tx, name string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?) ON CONFLICT DO NOTHING`, name)
	return wrap(err)
}

// pending returns the steps not in the ledger, in order (all of them if
// there's no ledger yet; none if there's no schema).
func (s schema) pending(ctx context.Context, q sqlx.QueryerContext) ([]Step, error) {
	if ok, err := s.Installed(ctx); err != nil || !ok {
		return nil, err
	}
	var done []string
	if err := sqlx.SelectContext(ctx, q, &done, `SELECT name FROM schema_migrations`); err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			return nil, wrap(err)
		}
	}
	have := make(map[string]bool, len(done))
	for _, n := range done {
		have[n] = true
	}
	var out []Step
	for _, st := range steps {
		if !have[st.Name] {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s schema) Pending(ctx context.Context) ([]string, error) {
	sts, err := s.pending(ctx, s.r)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sts))
	for _, st := range sts {
		out = append(out, st.Name)
	}
	return out, nil
}

// Migrate runs the pending steps in order, each in its own transaction
// with its ledger entry (SQLite DDL is transactional). Steps are recorded
// on every build, record or not: unlike upstream's beta migrations they
// don't change once tagged (see Step).
func (s schema) Migrate(ctx context.Context, env store.MigrationEnv, _ bool) error {
	if _, err := s.w.ExecContext(ctx, ledgerSQL); err != nil {
		return wrap(err)
	}
	sts, err := s.pending(ctx, s.w)
	if err != nil {
		return err
	}
	if len(sts) == 0 {
		env.Log.Printf("no upgrades to run. Database is up to date.")
		return nil
	}
	for _, st := range sts {
		env.Log.Printf("running migration %s", st.Name)
		err := s.inTx(ctx, func(tx *sqlx.Tx) error {
			if err := st.Fn(ctx, tx, env); err != nil {
				return err
			}
			return recordStep(ctx, tx, st.Name)
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", st.Name, err)
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
