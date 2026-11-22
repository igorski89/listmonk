package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/knadh/listmonk/models"
)

// AnalyticsSubject is what analytics are grouped by.
type AnalyticsSubject string

const (
	AnalyticsCampaigns AnalyticsSubject = "campaign"
	AnalyticsLists     AnalyticsSubject = "list"
)

// AnalyticsQuery is a time series request. Core validates Metric and
// Interval against models.AnalyticsType* / models.AnalyticsInterval*.
// From/To are inclusive. The HTTP handler already parses them into
// time.Time and upstream formats them back to RFC 3339 for Postgres, so
// passing time.Time here changes no behaviour.
type AnalyticsQuery struct {
	Subject  AnalyticsSubject
	IDs      []int
	Metric   string // views, clicks, total_views, total_clicks, bounces; lists also subscriptions, unsubscriptions
	From, To time.Time
	Interval string // hour, day, week, month
}

// AnalyticsStore is campaign and list analytics. Unique vs total counts
// follow Options.IndividualTracking, as upstream interpolates its count
// queries at startup today.
type AnalyticsStore interface {
	// Items returns the campaigns or lists that can be picked for
	// analytics, within scope.
	Items(ctx context.Context, subject AnalyticsSubject, ids []int, scope ListScope) ([]models.AnalyticsItem, error)

	// Counts returns one time series per ID.
	Counts(ctx context.Context, q AnalyticsQuery) ([]models.CampaignAnalyticsCount, error)

	// Links returns click counts per link for the campaigns or lists.
	Links(ctx context.Context, subject AnalyticsSubject, ids []int, from, to time.Time) ([]models.CampaignAnalyticsLink, error)

	// Activity returns per-subscriber views and clicks on campaigns.
	Activity(ctx context.Context, campIDs []int, from, to time.Time, scope ListScope, p Page) ([]models.CampaignActivity, error)

	// ActivityEvents returns one subscriber's individual view and click
	// events on campaigns.
	ActivityEvents(ctx context.Context, campIDs []int, from, to time.Time, subID int, p Page) ([]models.CampaignActivityEvent, error)
}

// StatsAggregate names a cached aggregate (a materialized view on
// Postgres, a table the store fills on SQLite).
type StatsAggregate string

const (
	StatsDashboardCounts StatsAggregate = "dashboard_counts"
	StatsDashboardCharts StatsAggregate = "dashboard_charts"
	StatsListSubscribers StatsAggregate = "list_subscriber_stats"
	StatsListCharts      StatsAggregate = "list_charts"
)

// StatsStore is the dashboard and list statistics that are expensive on
// large databases. With Options.CacheSlowQueries, reads
// return the last refreshed aggregate and Refresh (run from cron) updates
// it; otherwise every read is live. Results are JSON shaped for the
// frontend, identical across backends.
type StatsStore interface {
	DashboardCounts(ctx context.Context) (json.RawMessage, error)
	DashboardCharts(ctx context.Context) (json.RawMessage, error)

	// ListCharts returns a list's subscriber growth, or campaign views if
	// campaignViews.
	ListCharts(ctx context.Context, listID int, campaignViews bool) (json.RawMessage, error)

	// Refresh recomputes the given aggregates (none = all).
	Refresh(ctx context.Context, which ...StatsAggregate) error
}
