// Package postgres is the Postgres implementation of internal/store. It
// runs upstream listmonk's queries (queries/*.sql, prepared into
// models.Queries) unchanged; the code here is the database half of what
// used to live in internal/core and cmd.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/goyesql/v2"
	goyesqlx "github.com/knadh/goyesql/v2/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/knadh/stuffbin"
	"github.com/lib/pq"
	pgsess "github.com/zerodha/simplesessions/stores/postgres/v3"
)

// Migration is one upstream schema migration (internal/migrations).
type Migration struct {
	Version string
	Fn      func(*sqlx.DB, stuffbin.FileSystem, *koanf.Koanf, *log.Logger) error
}

// Store is a listmonk database on Postgres.
type Store struct {
	db   *sqlx.DB
	qMap goyesql.Queries
	q    *models.Queries
	opt  store.Options
	sess *pgsess.Store
	migs []Migration
	log  *log.Logger
}

var _ store.Store = (*Store)(nil)

// New returns a Store on an open connection pool. qMap is the parsed
// queries/*.sql; migs is the upstream migration list, in order. Call
// Configure before using anything but Settings and Schema.
func New(db *sqlx.DB, qMap goyesql.Queries, migs []Migration, lo *log.Logger) *Store {
	return &Store{db: db, qMap: qMap, migs: migs, log: lo}
}

// Configure prepares the queries, interpolating the ones that depend on
// settings, and opens the session store.
func (s *Store) Configure(o store.Options) error {
	q, err := prepareQueries(s.qMap, s.db, o.IndividualTracking)
	if err != nil {
		return err
	}
	s.q = q
	s.opt = o

	if o.SessionTTL > 0 {
		st, err := pgsess.New(pgsess.Opt{TTL: o.SessionTTL}, s.db.DB)
		if err != nil {
			return err
		}
		s.sess = st
	}
	return nil
}

func (s *Store) Dialect() string { return "postgres" }
func (s *Store) Close() error    { return s.db.Close() }

func (s *Store) Subscribers() store.SubscriberStore     { return subscribers{s} }
func (s *Store) Subscriptions() store.SubscriptionStore { return subscriptions{s} }
func (s *Store) Lists() store.ListStore                 { return lists{s} }
func (s *Store) Campaigns() store.CampaignStore         { return campaigns{s} }
func (s *Store) Tracking() store.TrackingStore          { return tracking{s} }
func (s *Store) Bounces() store.BounceStore             { return bounces{s} }
func (s *Store) Templates() store.TemplateStore         { return templates{s} }
func (s *Store) Media() store.MediaStore                { return mediaStore{s} }
func (s *Store) Settings() store.SettingsStore          { return settings{s} }
func (s *Store) Users() store.UserStore                 { return users{s} }
func (s *Store) Roles() store.RoleStore                 { return roles{s} }
func (s *Store) Sessions() store.SessionStore           { return s.sess }
func (s *Store) Analytics() store.AnalyticsStore        { return analytics{s} }
func (s *Store) Stats() store.StatsStore                { return stats{s} }
func (s *Store) Schema() store.SchemaStore              { return schema{s} }

// wrap turns a driver error into a store error: *pq.Error becomes
// *store.DBError (keeping Postgres' DETAIL); others pass through.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return &store.DBError{Err: err, Detail: pqErr.Detail}
	}
	return err
}

// pqErr returns the *pq.Error in err, if any.
func pqErr(err error) *pq.Error {
	var e *pq.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// notFound maps sql.ErrNoRows to store.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return wrap(err)
}

// sortExpr is the ORDER BY fragment upstream builds with
// "%order%" substitution: "<field> ASC|DESC".
func sortExpr(s store.Sort) string {
	if s.Desc {
		return s.Field + " " + models.OrderDesc
	}
	return s.Field + " " + models.OrderAsc
}

// prepareQueries prepares the query map into models.Queries. Moved from
// cmd/init.go: some count queries are interpolated depending on
// privacy.individual_tracking.
func prepareQueries(qMap goyesql.Queries, db *sqlx.DB, individualTracking bool) (*models.Queries, error) {
	var (
		countQuery = "get-campaign-analytics-counts"
		linkSel    = "*"
	)
	if individualTracking {
		countQuery = "get-campaign-analytics-unique-counts"
		linkSel = "DISTINCT subscriber_id"
	}

	// These don't exist in the SQL file but are in the queries struct to be prepared.
	qMap["get-campaign-view-counts"] = &goyesql.Query{
		Query: fmt.Sprintf(qMap[countQuery].Query, "campaign_views"),
		Tags:  map[string]string{"name": "get-campaign-view-counts"},
	}
	qMap["get-campaign-click-counts"] = &goyesql.Query{
		Query: fmt.Sprintf(qMap[countQuery].Query, "link_clicks"),
		Tags:  map[string]string{"name": "get-campaign-click-counts"},
	}
	for typ, table := range map[string]string{"view": "campaign_views", "click": "link_clicks"} {
		for _, resource := range []string{"campaign", "list"} {
			name := "get-" + resource + "-total-" + typ + "-counts"
			qMap[name] = &goyesql.Query{
				Query: fmt.Sprintf(qMap["get-"+resource+"-analytics-counts"].Query, table),
				Tags:  map[string]string{"name": name},
			}
		}
	}
	qMap["get-campaign-link-counts"].Query = fmt.Sprintf(qMap["get-campaign-link-counts"].Query, linkSel)

	// List metrics use the same tracking semantics, grouped by campaign lists.
	for typ, table := range map[string]string{"view": "campaign_views", "click": "link_clicks", "bounce": "bounces"} {
		query := "get-list-analytics-counts"
		if typ != "bounce" && individualTracking {
			query = "get-list-analytics-unique-counts"
		}
		name := "get-list-" + typ + "-counts"
		qMap[name] = &goyesql.Query{
			Query: fmt.Sprintf(qMap[query].Query, table),
			Tags:  map[string]string{"name": name},
		}
	}
	qMap["get-list-link-counts"].Query = fmt.Sprintf(qMap["get-list-link-counts"].Query, linkSel)

	// Scan and prepare all queries.
	var q models.Queries
	if err := goyesqlx.ScanToStruct(&q, qMap, db); err != nil {
		return nil, fmt.Errorf("error preparing SQL queries: %v", err)
	}
	return &q, nil
}

// txReadOnly begins a read-only transaction.
func (s *Store) txReadOnly(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
}
