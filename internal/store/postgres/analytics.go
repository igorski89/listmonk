package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type analytics struct{ *Store }
type stats struct{ *Store }

func (s analytics) Items(ctx context.Context, subject store.AnalyticsSubject, ids []int, scope store.ListScope) ([]models.AnalyticsItem, error) {
	out := []models.AnalyticsItem{}
	stmt := s.q.GetListAnalyticsItems
	if subject == store.AnalyticsCampaigns {
		stmt = s.q.GetCampaignAnalyticsItems
	}
	if err := stmt.SelectContext(ctx, &out, pq.Array(ids), scope.All, pq.Array(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s analytics) countStmt(subject store.AnalyticsSubject, metric string) *sqlx.Stmt {
	if subject == store.AnalyticsLists {
		switch metric {
		case models.AnalyticsTypeSubscriptions:
			return s.q.GetListSubscriptionCounts
		case models.AnalyticsTypeUnsubscriptions:
			return s.q.GetListUnsubscriptionCounts
		case models.AnalyticsTypeViews:
			return s.q.GetListViewCounts
		case models.AnalyticsTypeClicks:
			return s.q.GetListClickCounts
		case models.AnalyticsTypeTotalViews:
			return s.q.GetListTotalViewCounts
		case models.AnalyticsTypeTotalClicks:
			return s.q.GetListTotalClickCounts
		case models.AnalyticsTypeBounces:
			return s.q.GetListBounceCounts
		}
		return nil
	}

	switch metric {
	case models.AnalyticsTypeViews:
		return s.q.GetCampaignViewCounts
	case models.AnalyticsTypeClicks:
		return s.q.GetCampaignClickCounts
	case models.AnalyticsTypeTotalViews:
		return s.q.GetCampaignTotalViewCounts
	case models.AnalyticsTypeTotalClicks:
		return s.q.GetCampaignTotalClickCounts
	case models.AnalyticsTypeBounces:
		return s.q.GetCampaignBounceCounts
	}
	return nil
}

func (s analytics) Counts(ctx context.Context, q store.AnalyticsQuery) ([]models.CampaignAnalyticsCount, error) {
	stmt := s.countStmt(q.Subject, q.Metric)
	if stmt == nil {
		return nil, fmt.Errorf("unknown analytics metric %q for %s", q.Metric, q.Subject)
	}
	out := []models.CampaignAnalyticsCount{}
	if err := stmt.SelectContext(ctx, &out, pq.Array(q.IDs), q.From, q.To, q.Interval); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s analytics) Links(ctx context.Context, subject store.AnalyticsSubject, ids []int, from, to string) ([]models.CampaignAnalyticsLink, error) {
	stmt := s.q.GetCampaignLinkCounts
	if subject == store.AnalyticsLists {
		stmt = s.q.GetListLinkCounts
	}
	out := []models.CampaignAnalyticsLink{}
	if err := stmt.SelectContext(ctx, &out, pq.Array(ids), from, to); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s analytics) Activity(ctx context.Context, campIDs []int, from, to string, scope store.ListScope, p store.Page) ([]models.CampaignActivity, error) {
	out := []models.CampaignActivity{}
	if err := s.q.GetCampaignActivity.SelectContext(ctx, &out, pq.Array(campIDs), from, to, scope.All, pq.Array(scope.IDs), p.Offset, p.Limit); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s analytics) ActivityEvents(ctx context.Context, campIDs []int, from, to string, subID int, p store.Page) ([]models.CampaignActivityEvent, error) {
	out := []models.CampaignActivityEvent{}
	if err := s.q.GetCampaignActivityEvents.SelectContext(ctx, &out, pq.Array(campIDs), from, to, subID, p.Offset, p.Limit); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// Stats: the materialized views.

var matViews = map[store.StatsAggregate]string{
	store.StatsDashboardCharts: "mat_dashboard_charts",
	store.StatsDashboardCounts: "mat_dashboard_counts",
	store.StatsListSubscribers: "mat_list_subscriber_stats",
	store.StatsListCharts:      "mat_list_charts",
}

// refreshMatView refreshes a materialized view.
func (s *Store) refreshMatView(ctx context.Context, name string, concurrent bool) error {
	q := "REFRESH MATERIALIZED VIEW %s %s"
	if concurrent {
		q = fmt.Sprintf(q, "CONCURRENTLY", name)
	} else {
		q = fmt.Sprintf(q, "", name)
	}
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		s.log.Printf("error refreshing materialized view: %s: %v", name, err)
		return err
	}
	return nil
}

// refreshCache refreshes a materialized view if caching is disabled, so
// reads are live.
func (s *Store) refreshCache(ctx context.Context, a store.StatsAggregate) error {
	if s.opt.CacheSlowQueries {
		return nil
	}
	return s.refreshMatView(ctx, matViews[a], false)
}

func (s stats) DashboardCounts(ctx context.Context) (json.RawMessage, error) {
	_ = s.refreshCache(ctx, store.StatsDashboardCounts)
	var out types.JSONText
	if err := s.q.GetDashboardCounts.GetContext(ctx, &out); err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s stats) DashboardCharts(ctx context.Context) (json.RawMessage, error) {
	_ = s.refreshCache(ctx, store.StatsDashboardCharts)
	var out types.JSONText
	if err := s.q.GetDashboardCharts.GetContext(ctx, &out); err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s stats) ListCharts(ctx context.Context, listID int, campaignViews bool) (json.RawMessage, error) {
	var (
		out types.JSONText
		err error
	)
	if s.opt.CacheSlowQueries {
		err = s.q.GetListCharts.GetContext(ctx, &out, listID, campaignViews)
	} else {
		err = s.db.GetContext(ctx, &out, s.q.GetListChartsLive, listID, campaignViews)
	}
	if err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

// Refresh refreshes the given materialized views (all if none), concurrently,
// in upstream's order.
func (s stats) Refresh(ctx context.Context, which ...store.StatsAggregate) error {
	if len(which) == 0 {
		which = []store.StatsAggregate{store.StatsDashboardCharts, store.StatsDashboardCounts, store.StatsListSubscribers, store.StatsListCharts}
	}
	for _, a := range which {
		_ = s.refreshMatView(ctx, matViews[a], true)
	}
	return nil
}
