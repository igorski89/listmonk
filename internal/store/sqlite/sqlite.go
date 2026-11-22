package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schemaSQL string

// Store is a listmonk database in an SQLite file.
//
// All writes go through one connection (SQLite allows one writer; with a
// single-connection pool, writers queue in Go instead of failing with
// SQLITE_BUSY). Reads use a separate pool. Rule: inside a write
// transaction, never call another public store method (it would wait for
// the writer connection the transaction holds); use the tx helpers.
type Store struct {
	w   *sqlx.DB // writer: one connection
	r   *sqlx.DB // readers
	sb  *sqlx.DB // sandbox for user SQL expressions: read-only, query_only
	opt store.Options
	log *log.Logger

	configured bool
}

var _ store.Store = (*Store)(nil)

// Open opens (creating if needed) the database file at path.
func Open(path string, lo *log.Logger) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(10000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_time_format", "sqlite")
	// Times come back in UTC (as from Postgres with a UTC session), not in
	// a fixed +00:00 zone.
	q.Set("_timezone", "UTC")

	wq := url.Values{}
	for k, v := range q {
		wq[k] = v
	}
	wq.Set("_txlock", "immediate")

	w, err := sqlx.Connect("sqlite", "file:"+path+"?"+wq.Encode())
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)

	// The reader pool must never write: query_only makes a stray write
	// fail loudly (on every pooled connection, via the DSN).
	rq := url.Values{}
	for k, v := range q {
		rq[k] = v
	}
	rq.Add("_pragma", "query_only(1)")
	r, err := sqlx.Connect("sqlite", "file:"+path+"?"+rq.Encode())
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)

	sq := url.Values{}
	for k, v := range q {
		sq[k] = v
	}
	sq.Add("_pragma", "query_only(1)")
	sq.Set("mode", "ro")
	sb, err := sqlx.Connect("sqlite", "file:"+path+"?"+sq.Encode())
	if err != nil {
		w.Close()
		r.Close()
		return nil, err
	}
	sb.SetMaxOpenConns(2)

	return &Store{w: w.Unsafe(), r: r.Unsafe(), sb: sb.Unsafe(), log: lo}, nil
}

// Configure applies the settings-dependent options.
func (s *Store) Configure(o store.Options) error {
	s.opt = o
	s.configured = true
	return nil
}

func (s *Store) Dialect() string { return "sqlite" }

func (s *Store) Close() error {
	s.sb.Close()
	err1 := s.r.Close()
	err2 := s.w.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *Store) Subscribers() store.SubscriberStore     { return subscribers{s} }
func (s *Store) Subscriptions() store.SubscriptionStore { return subscriptions{s} }
func (s *Store) Lists() store.ListStore                 { return lists{s} }
func (s *Store) Campaigns() store.CampaignStore         { return campaigns{s} }
func (s *Store) Tracking() store.TrackingStore          { return tracking{s} }
func (s *Store) Bounces() store.BounceStore             { return bounces{s} }
func (s *Store) Templates() store.TemplateStore         { return templates{s} }
func (s *Store) Media() store.MediaStore                { return mediaStore{s} }
func (s *Store) Users() store.UserStore                 { return users{s} }
func (s *Store) Roles() store.RoleStore                 { return roles{s} }
func (s *Store) Sessions() store.SessionStore           { return sessions{s} }
func (s *Store) Analytics() store.AnalyticsStore        { return analytics{s} }
func (s *Store) Stats() store.StatsStore                { return stats{s} }
func (s *Store) Settings() store.SettingsStore          { return settings{s} }
func (s *Store) Schema() store.SchemaStore              { return schema{s} }

// Errors.

var reUniqueCols = regexp.MustCompile(`UNIQUE constraint failed: (.+)$`)

// conflictFields maps SQLite's "table.column" unique failures to the
// logical field, as the Postgres store maps its constraint names.
var conflictFields = map[string]string{
	"subscribers.email":      "email",
	"users.username":         "username",
	"users.email":            "email",
	"campaigns.archive_slug": "archive_slug",
	"roles.type, roles.name": "name",
}

// wrap turns a driver error into a store error: *sqlite.Error becomes
// *store.DBError, unique violations a *store.ConflictError around it.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var se *msqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	dbErr := &store.DBError{Err: err}
	if se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		field := "unknown"
		if m := reUniqueCols.FindStringSubmatch(se.Error()); m != nil {
			cols := strings.TrimSuffix(m[1], ")")
			if i := strings.Index(cols, " ("); i >= 0 {
				cols = cols[:i]
			}
			field = cols
			if f, ok := conflictFields[cols]; ok {
				field = f
			} else if strings.HasPrefix(cols, "index '") {
				// Expression indexes: report the index name, as Postgres
				// reports the constraint name.
				field = strings.Trim(strings.TrimPrefix(cols, "index "), "'")
			}
		}
		return &store.ConflictError{Field: field, Err: dbErr}
	}
	return dbErr
}

func asConflict(err error, ce **store.ConflictError) bool { return errors.As(err, ce) }

// isFKErr reports whether err is a foreign key violation.
// (ON DELETE RESTRICT reports SQLITE_CONSTRAINT_TRIGGER.)
func isFKErr(err error) bool {
	var se *msqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY ||
		(se.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER && strings.Contains(se.Error(), "FOREIGN KEY"))
}

// notFound maps sql.ErrNoRows to store.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return wrap(err)
}

// Values.

// tsLayout is the stored timestamp format (UTC). It matches the driver's
// "sqlite" time format and the schema's DEFAULT, and sorts in time order.
const tsLayout = "2006-01-02 15:04:05.999999999-07:00"

// ts formats a time for binding.
func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

// now is the current time for binding: one value per operation, so all
// rows an operation touches get the same timestamp (as Postgres' NOW()).
func now() string { return ts(time.Now()) }

// parseTS parses a stored timestamp (or one computed in SQL, which the
// driver returns as a string).
func parseTS(v string) (time.Time, error) {
	for _, l := range []string{tsLayout, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(l, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("sqlite: unparsable time %q", v)
}

// jsonText marshals v to JSON text for a TEXT column.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// strs marshals a string slice to a JSON array (never null).
func strs(v []string) string {
	if v == nil {
		v = []string{}
	}
	return jsonText(v)
}

// ints marshals an int slice to a JSON array (never null).
func ints(v []int) string {
	if v == nil {
		v = []int{}
	}
	return jsonText(v)
}

// jsonStrings scans a JSON array column into []string.
type jsonStrings []string

func (j *jsonStrings) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case nil:
		*j = nil
		return nil
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("sqlite: can't scan %T into []string", src)
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	*j = out
	return nil
}

// Value lets jsonStrings be bound as a JSON array.
func (j jsonStrings) Value() (driver.Value, error) { return strs(j), nil }

// jsonInt64s scans a JSON array column into []int64.
type jsonInt64s []int64

func (j *jsonInt64s) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case nil:
		*j = nil
		return nil
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("sqlite: can't scan %T into []int64", src)
	}
	var out []int64
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	*j = out
	return nil
}

// sortDir is "ASC" or "DESC".
func sortDir(s store.Sort) string {
	if s.Desc {
		return models.OrderDesc
	}
	return models.OrderAsc
}

// orderBy maps a logical sort field through a whitelist to an ORDER BY
// expression; unknown fields fall back to def. Never interpolate
// Sort.Field directly.
func orderBy(s store.Sort, fields map[string]string, def string) string {
	expr, ok := fields[s.Field]
	if !ok {
		expr = def
	}
	// Postgres puts NULLs last ascending and first descending.
	if s.Desc {
		return expr + " DESC NULLS FIRST"
	}
	return expr + " ASC NULLS LAST"
}

func replaceAll(s, old, new string) string { return strings.ReplaceAll(s, old, new) }

// withOrder fills a query's {ORDER} placeholder with an ORDER BY
// expression built by orderBy (never with user input).
func withOrder(query, order string) string {
	return strings.Replace(query, "{ORDER}", order, 1)
}

// inTx runs fn in a write transaction, committing if it returns nil.
func (s *Store) inTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := s.w.BeginTxx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return wrap(tx.Commit())
}
