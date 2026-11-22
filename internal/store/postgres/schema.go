package postgres

import (
	"context"
	"encoding/json"

	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"golang.org/x/mod/semver"
)

type schema struct{ *Store }

// IsTableNotExist reports whether err is Postgres' "relation does not
// exist" (42P01).
func IsTableNotExist(err error) bool {
	if e := pqErr(err); e != nil && e.Code == "42P01" {
		return true
	}
	return false
}

func (s schema) Installed(ctx context.Context) (bool, error) {
	if _, err := s.db.ExecContext(ctx, `SELECT id FROM templates LIMIT 1`); err != nil {
		if IsTableNotExist(err) {
			return false, nil
		}
		return false, wrap(err)
	}
	return true, nil
}

func (s schema) Install(ctx context.Context, version string, env store.MigrationEnv) error {
	q, err := env.FS.Read("/schema.sql")
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, string(q)); err != nil {
		return wrap(err)
	}

	// Insert the current migration version.
	return s.recordVersion(ctx, version)
}

// recordVersion appends a migration version to the `migrations` array in
// the settings table.
func (s schema) recordVersion(ctx context.Context, ver string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value)
					   VALUES('migrations', JSON_BUILD_ARRAY($1::TEXT))
					   ON CONFLICT (key) DO UPDATE SET value = settings.value || EXCLUDED.value`, ver)
	return wrap(err)
}

func (s schema) Version(ctx context.Context) (string, error) {
	var v string
	if err := s.db.GetContext(ctx, &v, `
		SELECT COALESCE(
			(SELECT value->>-1 FROM settings WHERE key='migrations'),
		'v0.0.0')`); err != nil {
		if IsTableNotExist(err) {
			return "v0.0.0", nil
		}
		return v, wrap(err)
	}
	return v, nil
}

// pending returns the migrations above the last recorded version.
func (s schema) pending(ctx context.Context) ([]Migration, error) {
	lastVer, err := s.Version(ctx)
	if err != nil {
		return nil, err
	}
	for i, m := range s.migs {
		if semver.Compare(m.Version, lastVer) > 0 {
			return s.migs[i:], nil
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
	return out, nil
}

func (s schema) Migrate(ctx context.Context, env store.MigrationEnv, record bool) error {
	migs, err := s.pending(ctx)
	if err != nil {
		return err
	}
	if len(migs) == 0 {
		env.Log.Printf("no upgrades to run. Database is up to date.")
		return nil
	}

	// Execute migrations in succession.
	for _, m := range migs {
		env.Log.Printf("running migration %s", m.Version)
		if err := m.Fn(s.db, env.FS, env.Config, env.Log); err != nil {
			return &MigrationError{Version: m.Version, Err: err}
		}

		// Record the migration version in the settings table. There was no
		// settings table until v0.7.0, so ignore the no-table errors.
		// For non-final builds, skip recording so migrations re-run on each boot.
		if record {
			if err := s.recordVersion(ctx, m.Version); err != nil {
				if IsTableNotExist(err) {
					continue
				}
				return &MigrationError{Version: m.Version, Record: true, Err: err}
			}
		}
	}
	env.Log.Printf("upgrade complete")
	return nil
}

func (s schema) Optimize(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "VACUUM ANALYZE")
	return wrap(err)
}

func (s schema) Info(ctx context.Context) (store.DBInfo, error) {
	out := store.DBInfo{Dialect: "postgres"}
	info := types.JSONText(`{}`)
	if err := s.db.QueryRowContext(ctx, s.qMap["get-db-info"].Query).Scan(&info); err != nil {
		return out, wrap(err)
	}
	var v struct {
		Version string  `json:"version"`
		SizeMB  float64 `json:"size_mb"`
	}
	if err := json.Unmarshal(info, &v); err != nil {
		return out, err
	}
	out.Version, out.SizeMB = v.Version, v.SizeMB
	return out, nil
}

// MigrationError is a failed migration.
type MigrationError struct {
	Version string
	Record  bool // failed recording the version, not running it
	Err     error
}

func (e *MigrationError) Error() string {
	if e.Record {
		return "error recording migration version " + e.Version + ": " + e.Err.Error()
	}
	return "error running migration " + e.Version + ": " + e.Err.Error()
}
func (e *MigrationError) Unwrap() error { return e.Err }
