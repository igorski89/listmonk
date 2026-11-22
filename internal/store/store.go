// Package store defines listmonk's storage layer: the per-domain interfaces
// that internal/core and the other app packages use instead of prepared
// statements, plus the types and errors shared by every backend.
//
// Rules for every interface in this package:
//
//   - One method is one complete operation. It is atomic and owns its own
//     transaction; callers never see sql.Tx, sqlx or driver types.
//   - Arguments and results are plain Go and models types. No pq.Array, no
//     Postgres array literals, no dialect SQL except a SubscriberFilter's
//     Expr, which is explicitly per backend.
//   - Errors are the ones below (wrapped with %w where useful). Callers use
//     errors.Is / errors.As; translating them to HTTP and i18n messages stays
//     in internal/core.
//   - Every method takes a context.Context first.
//
// Models are plain Go (decision 2026-11-22, option B): no pq array types
// and no driver-specific Scan methods in the types stores return. At
// v7.0.0-beta.2 that meant 7 fields: models.Campaign (Tags, MediaIDs),
// models.List (Tags), auth.User (UserRolePerms), auth.ListPermission and
// auth.Role (Permissions), media.Media (Tags). Upstream master adds
// Campaign.ListUUIDs, to be handled the same way on rebase. Each backend
// reads rows into its own row types, which embed the model and override
// only the columns it encodes differently (Postgres arrays, SQLite JSON
// text), then copies those into the model.
//
// Implementations live in store/postgres (wrapping upstream's queries/*.sql
// unchanged) and store/sqlite. Both must pass the conformance suite in
// store/storetest.
package store

import (
	"errors"
	"fmt"
	"time"
)

// Store is one open database. It hands out the per-domain stores, all
// sharing the same connections.
type Store interface {
	Subscribers() SubscriberStore
	Subscriptions() SubscriptionStore
	Lists() ListStore
	Campaigns() CampaignStore
	Tracking() TrackingStore
	Bounces() BounceStore
	Templates() TemplateStore
	Media() MediaStore
	Settings() SettingsStore
	Users() UserStore
	Roles() RoleStore
	Sessions() SessionStore
	Analytics() AnalyticsStore
	Stats() StatsStore
	Schema() SchemaStore

	// Configure applies the options that depend on settings stored in the
	// database. Startup: open the backend, read Settings().All(), build
	// Options, call Configure once, then use the other stores. (Upstream
	// does the same: initSettings, then prepareQueries interpolates by
	// privacy.individual_tracking.) Settings() and Schema() work before
	// Configure; the others return an error until it has been called.
	Configure(o Options) error

	// Dialect names the backend ("postgres", "sqlite"). Used for logging
	// and to tell users which dialect a SubscriberFilter.Expr must be in;
	// never to branch app logic.
	Dialect() string

	Close() error
}

// Options are the settings-dependent behaviours of a store.
type Options struct {
	// IndividualTracking: analytics count unique subscribers, not just
	// totals (privacy.individual_tracking).
	IndividualTracking bool

	// CacheSlowQueries: dashboard and list stats come from aggregates
	// refreshed by StatsStore.Refresh instead of live queries
	// (app.cache_slow_queries).
	CacheSlowQueries bool

	// SessionTTL: login session lifetime, for SessionStore.Prune.
	SessionTTL time.Duration
}

// Errors returned by every store. Backends map their driver errors to these;
// nothing above the store inspects *pq.Error or sqlite error codes.
var (
	// ErrNotFound: the addressed row does not exist.
	ErrNotFound = errors.New("store: not found")
)

// ErrInUse: the row can't be deleted because others still reference it
// (e.g. a role assigned to users).
var ErrInUse = errors.New("store: in use")

// DBError is a database error the store doesn't map to one of the errors
// above. Error() is the driver's message; Detail is the driver's extra
// detail, if any (Postgres' DETAIL), for messages shown to admins.
type DBError struct {
	Err    error
	Detail string
}

func (e *DBError) Error() string { return e.Err.Error() }
func (e *DBError) Unwrap() error { return e.Err }

// ConflictError is a unique constraint violation. Field is the logical
// field ("email", "archive_slug", "name"), not a constraint name.
type ConflictError struct {
	Field string
	Err   error
}

func (e *ConflictError) Error() string { return fmt.Sprintf("store: %s already exists", e.Field) }
func (e *ConflictError) Unwrap() error { return e.Err }

// InvalidQueryError is a rejected SubscriberFilter.Expr: syntax error,
// forbidden table or function, or a statement that is not read-only.
// Msg is safe to show to the user who wrote the expression.
type InvalidQueryError struct {
	Msg string
	Err error
}

func (e *InvalidQueryError) Error() string { return "store: invalid query: " + e.Msg }
func (e *InvalidQueryError) Unwrap() error { return e.Err }

// Page is offset pagination. Limit <= 0 means no limit.
type Page struct {
	Offset int
	Limit  int
}

// Sort is an ordering on a logical field. Core validates Field against its
// whitelist; each backend maps it to a column and rejects unknown fields.
type Sort struct {
	Field string
	Desc  bool
}
