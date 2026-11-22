package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

type analytics struct{ *Store }
type stats struct{ *Store }
type bounces struct{ *Store }

// sqlTime scans a timestamp computed in SQL (returned as a string) or a
// TIMESTAMP column (returned as time.Time).
type sqlTime struct{ time.Time }

func (t *sqlTime) Scan(src any) error {
	switch v := src.(type) {
	case time.Time:
		t.Time = v
	case string:
		p, err := parseTS(v)
		if err != nil {
			return err
		}
		t.Time = p
	case []byte:
		p, err := parseTS(string(v))
		if err != nil {
			return err
		}
		t.Time = p
	case nil:
		t.Time = time.Time{}
	default:
		return fmt.Errorf("sqlite: can't scan %T into time", src)
	}
	return nil
}

// boundTS turns a client's date or timestamp (as Postgres would cast it
// to timestamptz) into the stored text form.
func boundTS(v string) (string, error) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(l, v); err == nil {
			return ts(t), nil
		}
	}
	return "", fmt.Errorf("invalid date: %q", v)
}

// trunc is Postgres' DATE_TRUNC(interval, col) as stored-format text.
func trunc(interval, col string) (string, error) {
	switch interval {
	case models.AnalyticsIntervalHour:
		return "strftime('%Y-%m-%d %H:00:00+00:00', " + col + ")", nil
	case models.AnalyticsIntervalDay:
		return "(date(" + col + ") || ' 00:00:00+00:00')", nil
	case models.AnalyticsIntervalWeek:
		return "(date(" + col + ", 'weekday 0', '-6 days') || ' 00:00:00+00:00')", nil
	case models.AnalyticsIntervalMonth:
		return "strftime('%Y-%m-01 00:00:00+00:00', " + col + ")", nil
	}
	return "", fmt.Errorf("invalid interval %q", interval)
}

func (s analytics) Items(ctx context.Context, subject store.AnalyticsSubject, ids []int, scope store.ListScope) ([]models.AnalyticsItem, error) {
	out := []models.AnalyticsItem{}
	q := `SELECT l.id, l.name FROM json_each(?1) j JOIN lists l ON (l.id = j.value)
		WHERE (?2 OR l.id IN (SELECT value FROM json_each(?3))) ORDER BY j.key`
	if subject == store.AnalyticsCampaigns {
		q = `SELECT c.id, c.name, c.sent FROM json_each(?1) j JOIN campaigns c ON (c.id = j.value)
			WHERE (?2 OR EXISTS (SELECT 1 FROM campaign_lists WHERE campaign_id = c.id AND list_id IN (SELECT value FROM json_each(?3))))
			ORDER BY j.key`
	}
	if err := s.r.SelectContext(ctx, &out, q, ints(ids), scope.All, ints(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

type countRow struct {
	models.CampaignAnalyticsCount
	Timestamp sqlTime `db:"timestamp"`
}

func (s analytics) Counts(ctx context.Context, q store.AnalyticsQuery) ([]models.CampaignAnalyticsCount, error) {
	tr, err := trunc(q.Interval, "created_at")
	if err != nil {
		return nil, err
	}
	from, err := boundTS(q.From)
	if err != nil {
		return nil, err
	}
	to, err := boundTS(q.To)
	if err != nil {
		return nil, err
	}

	var table string
	switch q.Metric {
	case models.AnalyticsTypeViews, models.AnalyticsTypeTotalViews:
		table = "campaign_views"
	case models.AnalyticsTypeClicks, models.AnalyticsTypeTotalClicks:
		table = "link_clicks"
	case models.AnalyticsTypeBounces:
		table = "bounces"
	case models.AnalyticsTypeSubscriptions, models.AnalyticsTypeUnsubscriptions:
		if q.Subject != store.AnalyticsLists {
			return nil, fmt.Errorf("unknown analytics metric %q for %s", q.Metric, q.Subject)
		}
	default:
		return nil, fmt.Errorf("unknown analytics metric %q for %s", q.Metric, q.Subject)
	}
	unique := s.opt.IndividualTracking && (q.Metric == models.AnalyticsTypeViews || q.Metric == models.AnalyticsTypeClicks)

	var sqlq string
	switch {
	case q.Metric == models.AnalyticsTypeSubscriptions:
		sqlq = `SELECT list_id, COUNT(*) AS count, ` + tr + ` AS timestamp FROM subscriber_lists
			WHERE list_id IN (SELECT value FROM json_each(?1)) AND created_at >= ?2 AND created_at <= ?3
			GROUP BY list_id, timestamp ORDER BY timestamp`
	case q.Metric == models.AnalyticsTypeUnsubscriptions:
		trU, _ := trunc(q.Interval, "updated_at")
		sqlq = `SELECT list_id, COUNT(*) AS count, ` + trU + ` AS timestamp FROM subscriber_lists
			WHERE list_id IN (SELECT value FROM json_each(?1)) AND status = 'unsubscribed' AND updated_at >= ?2 AND updated_at <= ?3
			GROUP BY list_id, timestamp ORDER BY timestamp`
	case q.Subject == store.AnalyticsCampaigns:
		events := `SELECT campaign_id, ` + tr + ` AS timestamp FROM ` + table + `
			WHERE campaign_id IN (SELECT value FROM json_each(?1)) AND created_at >= ?2 AND created_at <= ?3`
		if unique {
			// The first event per subscriber and campaign.
			events = `SELECT campaign_id, MIN(` + tr + `) AS timestamp FROM ` + table + `
				WHERE campaign_id IN (SELECT value FROM json_each(?1)) AND created_at >= ?2 AND created_at <= ?3
				GROUP BY subscriber_id, campaign_id`
		}
		sqlq = `SELECT campaign_id, COUNT(*) AS count, timestamp FROM (` + events + `)
			GROUP BY campaign_id, timestamp ORDER BY timestamp`
	default:
		// Lists: events of the lists' campaigns, summed per list, with the
		// combined total before shared campaigns are counted per list.
		events := `SELECT campaign_id, ` + tr + ` AS timestamp FROM ` + table + `
			WHERE campaign_id IN (SELECT campaign_id FROM campaign_lists WHERE list_id IN (SELECT value FROM json_each(?1)))
			AND created_at >= ?2 AND created_at <= ?3`
		if unique {
			events = `SELECT campaign_id, MIN(` + tr + `) AS timestamp FROM ` + table + `
				WHERE campaign_id IN (SELECT campaign_id FROM campaign_lists WHERE list_id IN (SELECT value FROM json_each(?1)))
				AND created_at >= ?2 AND created_at <= ?3
				GROUP BY subscriber_id, campaign_id`
		}
		sqlq = `WITH counts AS (SELECT campaign_id, COUNT(*) AS count, timestamp FROM (` + events + `) GROUP BY campaign_id, timestamp)
			SELECT cl.list_id, SUM(c.count) AS count, c.timestamp, (SELECT SUM(count) FROM counts) AS total
			FROM counts c JOIN campaign_lists cl ON (cl.campaign_id = c.campaign_id)
			WHERE cl.list_id IN (SELECT value FROM json_each(?1))
			GROUP BY cl.list_id, c.timestamp ORDER BY c.timestamp`
	}

	var rows []countRow
	if err := s.r.SelectContext(ctx, &rows, sqlq, ints(q.IDs), from, to); err != nil {
		return nil, wrap(err)
	}
	out := make([]models.CampaignAnalyticsCount, len(rows))
	for i, r := range rows {
		out[i] = r.CampaignAnalyticsCount
		out[i].Timestamp = r.Timestamp.Time
	}
	return out, nil
}

func (s analytics) Links(ctx context.Context, subject store.AnalyticsSubject, ids []int, from, to string) ([]models.CampaignAnalyticsLink, error) {
	f, err := boundTS(from)
	if err != nil {
		return nil, err
	}
	t, err := boundTS(to)
	if err != nil {
		return nil, err
	}
	count := "COUNT(*)"
	if s.opt.IndividualTracking {
		count = "COUNT(DISTINCT subscriber_id)"
	}
	where := "campaign_id IN (SELECT value FROM json_each(?1))"
	if subject == store.AnalyticsLists {
		where = "campaign_id IN (SELECT campaign_id FROM campaign_lists WHERE list_id IN (SELECT value FROM json_each(?1)))"
	}
	out := []models.CampaignAnalyticsLink{}
	if err := s.r.SelectContext(ctx, &out, `SELECT `+count+` AS count, url FROM link_clicks
		LEFT JOIN links ON (link_clicks.link_id = links.id)
		WHERE `+where+` AND link_clicks.created_at >= ?2 AND link_clicks.created_at <= ?3
		GROUP BY links.url ORDER BY count DESC LIMIT 50`, ints(ids), f, t); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

type activityRow struct {
	models.CampaignActivity
	LastActivity sqlTime `db:"last_activity"`
}

func (s analytics) Activity(ctx context.Context, campIDs []int, from, to string, scope store.ListScope, p store.Page) ([]models.CampaignActivity, error) {
	f, err := boundTS(from)
	if err != nil {
		return nil, err
	}
	t, err := boundTS(to)
	if err != nil {
		return nil, err
	}
	var rows []activityRow
	if err := s.r.SelectContext(ctx, &rows, `WITH events AS (
		SELECT subscriber_id, 'views' AS type, created_at FROM campaign_views
			WHERE campaign_id IN (SELECT value FROM json_each(?1)) AND created_at BETWEEN ?2 AND ?3 AND subscriber_id IS NOT NULL
		UNION ALL
		SELECT subscriber_id, 'clicks', created_at FROM link_clicks
			WHERE campaign_id IN (SELECT value FROM json_each(?1)) AND created_at BETWEEN ?2 AND ?3 AND subscriber_id IS NOT NULL
	)
	SELECT s.id, s.email, s.name,
		COUNT(*) FILTER (WHERE e.type = 'views') AS views,
		COUNT(*) FILTER (WHERE e.type = 'clicks') AS clicks,
		MAX(e.created_at) AS last_activity, COUNT(*) OVER () AS total
	FROM events e JOIN subscribers s ON (s.id = e.subscriber_id)
	WHERE (?4 OR EXISTS (SELECT 1 FROM subscriber_lists WHERE subscriber_id = s.id AND list_id IN (SELECT value FROM json_each(?5))))
	GROUP BY s.id ORDER BY clicks DESC, views DESC, last_activity DESC, s.id
	LIMIT ?7 OFFSET ?6`, ints(campIDs), f, t, scope.All, ints(scope.IDs), p.Offset, limit(p.Limit)); err != nil {
		return nil, wrap(err)
	}
	out := make([]models.CampaignActivity, len(rows))
	for i, r := range rows {
		out[i] = r.CampaignActivity
		out[i].LastActivity = r.LastActivity.Time
	}
	return out, nil
}

type eventRow struct {
	models.CampaignActivityEvent
	CreatedAt sqlTime `db:"created_at"`
}

func (s analytics) ActivityEvents(ctx context.Context, campIDs []int, from, to string, subID int, p store.Page) ([]models.CampaignActivityEvent, error) {
	f, err := boundTS(from)
	if err != nil {
		return nil, err
	}
	t, err := boundTS(to)
	if err != nil {
		return nil, err
	}
	var rows []eventRow
	if err := s.r.SelectContext(ctx, &rows, `WITH events AS (
		SELECT id, campaign_id, 'views' AS type, '' AS url, created_at FROM campaign_views
			WHERE campaign_id IN (SELECT value FROM json_each(?1)) AND created_at BETWEEN ?2 AND ?3 AND subscriber_id = ?4
		UNION ALL
		SELECT lc.id, lc.campaign_id, 'clicks', l.url, lc.created_at FROM link_clicks lc JOIN links l ON (l.id = lc.link_id)
			WHERE lc.campaign_id IN (SELECT value FROM json_each(?1)) AND lc.created_at BETWEEN ?2 AND ?3 AND lc.subscriber_id = ?4
	)
	SELECT c.id AS campaign_id, c.name AS campaign, c.subject AS campaign_subject, e.type, e.url, e.created_at, COUNT(*) OVER () AS total
	FROM events e JOIN campaigns c ON (c.id = e.campaign_id)
	ORDER BY e.created_at DESC, e.type, e.id DESC LIMIT ?6 OFFSET ?5`, ints(campIDs), f, t, subID, p.Offset, limit(p.Limit)); err != nil {
		return nil, wrap(err)
	}
	out := make([]models.CampaignActivityEvent, len(rows))
	for i, r := range rows {
		out[i] = r.CampaignActivityEvent
		out[i].CreatedAt = r.CreatedAt.Time
	}
	return out, nil
}

// Stats: computed live (Postgres caches them in materialized views when
// app.cache_slow_queries is on; Refresh is a no-op here).

func (s stats) DashboardCounts(ctx context.Context) (json.RawMessage, error) {
	var out []byte
	err := s.r.GetContext(ctx, &out, `WITH subs AS (SELECT COUNT(*) AS num, status FROM subscribers GROUP BY status)
	SELECT CAST(json_object(
		'subscribers', json_object(
			'total', (SELECT SUM(num) FROM subs),
			'blocklisted', (SELECT num FROM subs WHERE status = 'blocklisted'),
			'orphans', (SELECT COUNT(id) FROM subscribers LEFT JOIN subscriber_lists ON (subscribers.id = subscriber_lists.subscriber_id)
				WHERE subscriber_lists.subscriber_id IS NULL)
		),
		'lists', json_object(
			'total', (SELECT COUNT(*) FROM lists),
			'private', (SELECT COUNT(*) FROM lists WHERE type = 'private'),
			'public', (SELECT COUNT(*) FROM lists WHERE type = 'public'),
			'optin_single', (SELECT COUNT(*) FROM lists WHERE optin = 'single'),
			'optin_double', (SELECT COUNT(*) FROM lists WHERE optin = 'double')
		),
		'campaigns', json_object(
			'total', (SELECT COUNT(*) FROM campaigns),
			'by_status', json((SELECT json_group_object(status, num) FROM (SELECT status, COUNT(*) AS num FROM campaigns GROUP BY status)))
		),
		'messages', (SELECT SUM(sent) FROM campaigns)
	) AS BLOB)`)
	if err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

// dailyCounts is the last 30 days of a table's events, counted per day,
// ending on the day of its latest event (mat_dashboard_charts).
func dailyCounts(table string) string {
	return `(SELECT COALESCE(json_group_array(json_object('count', count, 'date', date)), '[]') FROM (
		SELECT COUNT(*) AS count, date(created_at) AS date FROM ` + table + `
		WHERE created_at >= (SELECT date(created_at, '-30 days') FROM ` + table + ` ORDER BY id DESC LIMIT 1)
		AND date(created_at) <= (SELECT date(created_at) FROM ` + table + ` ORDER BY id DESC LIMIT 1)
		GROUP BY date ORDER BY date))`
}

func (s stats) DashboardCharts(ctx context.Context) (json.RawMessage, error) {
	var out []byte
	err := s.r.GetContext(ctx, &out, `SELECT CAST(json_object('link_clicks', json(`+dailyCounts("link_clicks")+`),
		'campaign_views', json(`+dailyCounts("campaign_views")+`)) AS BLOB)`)
	if err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s stats) ListCharts(ctx context.Context, listID int, campaignViews bool) (json.RawMessage, error) {
	var out []byte
	err := s.r.GetContext(ctx, &out, `WITH RECURSIVE `+weeksCTE("-3 months")+`,
	subscriptions AS (
		SELECT `+week("created_at")+` AS date, COUNT(*) AS count FROM subscriber_lists
		WHERE list_id = ?1 AND created_at >= date('now', '-3 months') AND created_at <= ?3 GROUP BY 1
	), unsubscriptions AS (
		SELECT `+week("updated_at")+` AS date, COUNT(*) AS count FROM subscriber_lists
		WHERE list_id = ?1 AND status = 'unsubscribed' AND updated_at >= date('now', '-3 months') AND updated_at <= ?3 GROUP BY 1
	), views AS (
		SELECT `+week("v.created_at")+` AS date, COUNT(*) AS count FROM campaign_views v JOIN campaign_lists cl ON (cl.campaign_id = v.campaign_id)
		WHERE ?2 AND cl.list_id = ?1 AND v.created_at >= date('now', '-3 months') AND v.created_at <= ?3 GROUP BY 1
	)
	SELECT CAST(json_object(
		'unsubscriptions', json_group_array(json_object('date', w.date, 'count', COALESCE(u.count, 0)) ORDER BY w.date),
		'subscriptions', json_group_array(json_object('date', w.date, 'count', COALESCE(sb.count, 0)) ORDER BY w.date),
		'campaign_views', CASE WHEN ?2 THEN json_group_array(json_object('date', w.date, 'count', COALESCE(v.count, 0)) ORDER BY w.date)
			ELSE json('[]') END
	) AS BLOB)
	FROM weeks w LEFT JOIN subscriptions sb ON (sb.date = w.date) LEFT JOIN unsubscriptions u ON (u.date = w.date)
	LEFT JOIN views v ON (v.date = w.date)`, listID, campaignViews, now())
	if err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s stats) Refresh(ctx context.Context, which ...store.StatsAggregate) error { return nil }

// Bounces.

type bounceRow struct {
	models.Bounce
	Meta     types.JSONText `db:"meta"`
	Campaign types.JSONText `db:"campaign"`
}

func (r bounceRow) model() models.Bounce {
	b := r.Bounce
	b.Meta = json.RawMessage(r.Meta)
	if r.Campaign != nil {
		c := json.RawMessage(r.Campaign)
		b.Campaign = &c
	}
	return b
}

func (s bounces) Record(ctx context.Context, b models.Bounce, action store.BounceAction) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var sub struct {
			ID     int    `db:"id"`
			Status string `db:"status"`
		}
		if err := tx.GetContext(ctx, &sub, `SELECT id, status FROM subscribers WHERE CASE WHEN ?1 != '' THEN uuid = ?1 ELSE email = ?2 END`,
			b.SubscriberUUID, b.Email); err != nil {
			return notFound(err)
		}
		var num int
		if err := tx.GetContext(ctx, &num, `SELECT COUNT(*) + 1 FROM bounces WHERE subscriber_id = ? AND type = ?`, sub.ID, b.Type); err != nil {
			return wrap(err)
		}
		reached := num >= action.Count
		t := now()
		if action.Action == "blocklist" && reached && sub.Status != models.SubscriberStatusBlockListed {
			if _, err := tx.ExecContext(ctx, `UPDATE subscribers SET status = 'blocklisted' WHERE id = ?`, sub.ID); err != nil {
				return wrap(err)
			}
		}
		if action.Action == "unsubscribe" && reached && sub.Status != models.SubscriberStatusBlockListed {
			if _, err := tx.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'unsubscribed' WHERE subscriber_id = ?`, sub.ID); err != nil {
				return wrap(err)
			}
		}
		// Record the bounce unless the subscriber was already blocklisted
		// or is past the threshold.
		if sub.Status != models.SubscriberStatusBlockListed && num <= action.Count {
			meta := string(b.Meta)
			if meta == "" {
				meta = "{}"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO bounces (subscriber_id, campaign_id, type, source, meta, created_at)
				VALUES (?1, (SELECT id FROM campaigns WHERE ?2 != '' AND uuid = ?2), ?3, ?4, json(?5), ?6)`,
				sub.ID, b.CampaignUUID, b.Type, b.Source, meta, ts(b.CreatedAt)); err != nil {
				return wrap(err)
			}
		}
		if action.Action == "delete" && reached {
			if _, err := tx.ExecContext(ctx, `DELETE FROM subscribers WHERE id = ?`, sub.ID); err != nil {
				return wrap(err)
			}
		}
		_ = t
		return nil
	})
}

var bounceSortFields = map[string]string{
	"email":         "subscribers.email",
	"campaign_name": "campaigns.name",
	"source":        "bounces.source",
	"created_at":    "bounces.created_at",
	"type":          `CASE bounces.type WHEN 'soft' THEN 0 WHEN 'hard' THEN 1 WHEN 'complaint' THEN 2 END`,
}

const bouncesSQL = `SELECT COUNT(*) OVER () AS total, bounces.id, bounces.type, bounces.source,
	CAST(bounces.meta AS BLOB) AS meta, bounces.created_at, bounces.subscriber_id,
	subscribers.uuid AS subscriber_uuid, subscribers.email AS email, subscribers.name AS subscriber_name,
	subscribers.status AS subscriber_status,
	CAST(CASE WHEN bounces.campaign_id IS NOT NULL THEN json_object('id', bounces.campaign_id, 'name', campaigns.name) ELSE NULL END AS BLOB) AS campaign
	FROM bounces
	LEFT JOIN subscribers ON (subscribers.id = bounces.subscriber_id)
	LEFT JOIN campaigns ON (campaigns.id = bounces.campaign_id)
	WHERE (?1 = 0 OR bounces.id = ?1) AND (?2 = 0 OR bounces.campaign_id = ?2) AND (?3 = 0 OR bounces.subscriber_id = ?3)
	AND (?4 = '' OR bounces.source = ?4) AND (?5 = '' OR bounces.type = ?5)
	AND (?8 OR EXISTS (SELECT 1 FROM subscriber_lists WHERE subscriber_id = bounces.subscriber_id AND list_id IN (SELECT value FROM json_each(?9))))
	ORDER BY {ORDER} LIMIT ?7 OFFSET ?6`

func (s bounces) Query(ctx context.Context, f store.BounceFilter, srt store.Sort, p store.Page) ([]models.Bounce, int, error) {
	var rows []bounceRow
	q := withOrder(bouncesSQL, orderBy(srt, bounceSortFields, "bounces.created_at")+", bounces.id "+sortDir(srt))
	if err := s.r.SelectContext(ctx, &rows, q, 0, f.CampaignID, f.SubscriberID, f.Source, f.Type, p.Offset, limit(p.Limit),
		f.Scope.All, ints(f.Scope.IDs)); err != nil {
		return nil, 0, wrap(err)
	}
	out := make([]models.Bounce, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s bounces) Get(ctx context.Context, id int, scope store.ListScope) (models.Bounce, error) {
	var rows []bounceRow
	if err := s.r.SelectContext(ctx, &rows, withOrder(bouncesSQL, "bounces.id"), id, 0, 0, "", "", 0, 1, scope.All, ints(scope.IDs)); err != nil {
		return models.Bounce{}, wrap(err)
	}
	if len(rows) == 0 {
		return models.Bounce{}, store.ErrNotFound
	}
	return rows[0].model(), nil
}

// bounceWhere selects bounces by IDs, or (with all) by the filter fields,
// within scope. ?1 IDs, ?2 all, ?3 campaign, ?4 source, ?5 type, ?6 all
// lists, ?7 list IDs.
const bounceWhere = `(CASE WHEN json_array_length(?1) > 0 THEN id IN (SELECT value FROM json_each(?1))
		WHEN ?2 THEN (?3 = 0 OR campaign_id = ?3) AND (?4 = '' OR source = ?4) AND (?5 = '' OR type = ?5)
		ELSE 0 END)
	AND (?6 OR EXISTS (SELECT 1 FROM subscriber_lists WHERE subscriber_id = bounces.subscriber_id AND list_id IN (SELECT value FROM json_each(?7))))`

func (s bounces) BlocklistSubscribers(ctx context.Context, f store.BounceFilter) error {
	if len(f.IDs) == 0 && !f.All {
		return nil
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var ids []int
		if err := tx.SelectContext(ctx, &ids, `SELECT DISTINCT subscriber_id FROM bounces WHERE `+bounceWhere,
			ints(f.IDs), f.All, f.CampaignID, f.Source, f.Type, f.Scope.All, ints(f.Scope.IDs)); err != nil {
			return wrap(err)
		}
		return blocklistIDs(ctx, tx, ids)
	})
}

func (s bounces) Delete(ctx context.Context, f store.BounceFilter) (int, error) {
	// upstream: with All, the filter fields apply even if IDs are set.
	ids := f.IDs
	if f.All {
		ids = nil
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM bounces WHERE `+bounceWhere,
		ints(ids), f.All, f.CampaignID, f.Source, f.Type, f.Scope.All, ints(f.Scope.IDs))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s bounces) DeleteForSubscriber(ctx context.Context, ref store.SubscriberRef) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM bounces WHERE subscriber_id = (SELECT id FROM subscribers WHERE CASE WHEN ?1 > 0 THEN id = ?1 ELSE uuid = ?2 END)`,
		ref.ID, ref.UUID)
	return wrap(err)
}
