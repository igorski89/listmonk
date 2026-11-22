package store

import (
	"context"
	"log"

	"github.com/knadh/koanf/v2"
	"github.com/knadh/stuffbin"
)

// MigrationEnv is what migrations may use besides the database: the
// embedded files (templates, static assets) and the config. The one place
// this package takes app types: upstream migrations need both.
type MigrationEnv struct {
	FS     stuffbin.FileSystem
	Config *koanf.Koanf
	Log    *log.Logger
}

// DBInfo is shown on the admin "About" page.
type DBInfo struct {
	Dialect string  `json:"-"`
	Version string  `json:"version"`
	SizeMB  float64 `json:"size_mb"`
}

// SchemaStore is the database's own lifecycle. Each backend owns its
// schema and its migration chain: Postgres runs upstream's schema.sql and
// internal/migrations unchanged; SQLite has a baseline schema at the
// version it first ships in and its own migration for every later
// upstream one.
type SchemaStore interface {
	// Installed reports whether the schema exists.
	Installed(ctx context.Context) (bool, error)

	// Install (re)creates the schema and default settings (Postgres:
	// /schema.sql from env.FS) and records version as the schema version. It DROPS existing listmonk tables
	// first (upstream schema.sql does), so callers confirm with the user,
	// as cmd/install.go does today. The first user role (Super Admin)
	// gets ID 1. It does not add sample data: cmd/install.go
	// creates the sample lists, subscribers, templates and campaign
	// through the other stores, once, for every backend.
	Install(ctx context.Context, version string, env MigrationEnv) error

	// Version returns the last recorded schema version ("v0.0.0" if none).
	Version(ctx context.Context) (string, error)

	// Pending returns the versions of the known migrations newer than the
	// recorded one, in order.
	Pending(ctx context.Context) ([]string, error)

	// Migrate runs the pending migrations in order. With record, the
	// version is recorded after each (non-final builds don't record, so
	// their migrations re-run on every start, as upstream). Not
	// transactional: upstream migrations take the DB handle and are
	// written to be re-runnable (IF NOT EXISTS etc.), and SQLite's will
	// follow the same rule.
	Migrate(ctx context.Context, env MigrationEnv, record bool) error

	// Optimize is the periodic maintenance job (Postgres VACUUM ANALYZE;
	// SQLite PRAGMA optimize, incremental vacuum).
	Optimize(ctx context.Context) error

	Info(ctx context.Context) (DBInfo, error)
}
