package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

type subscribers struct{ *Store }
type subscriptions struct{ *Store }

// jts renders a stored timestamp column as ISO 8601 inside JSON built in
// SQL ("2006-01-02T15:04:05.123+00:00"), like Postgres' row_to_json.
func jts(col string) string { return "replace(" + col + ", ' ', 'T')" }

// The JSON of a subscriber's lists (get-subscriber-lists-lazy).
var subListsSQL = `SELECT sl.subscriber_id, CAST(json_group_array(json_object(
		'subscription_status', sl.status,
		'subscription_created_at', ` + jts("sl.created_at") + `,
		'subscription_updated_at', ` + jts("sl.updated_at") + `,
		'subscription_meta', json(sl.meta),
		'id', l.id, 'uuid', l.uuid, 'name', l.name, 'type', l.type, 'optin', l.optin, 'status', l.status,
		'tags', json(l.tags), 'description', l.description,
		'created_at', ` + jts("l.created_at") + `, 'updated_at', ` + jts("l.updated_at") + `
	) ORDER BY sl.created_at, sl.list_id) AS BLOB) AS lists
	FROM subscriber_lists sl JOIN lists l ON (l.id = sl.list_id)
	WHERE sl.subscriber_id IN (SELECT value FROM json_each(?1))
	GROUP BY sl.subscriber_id`

// loadLists attaches each subscriber's lists JSON ([] if none).
func (s *Store) loadLists(ctx context.Context, q sqlx.QueryerContext, subs models.Subscribers) error {
	if len(subs) == 0 {
		return nil
	}
	var rows []struct {
		SubscriberID int            `db:"subscriber_id"`
		Lists        types.JSONText `db:"lists"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, subListsSQL, ints(subs.GetIDs())); err != nil {
		return wrap(err)
	}
	byID := make(map[int]types.JSONText, len(rows))
	for _, r := range rows {
		byID[r.SubscriberID] = r.Lists
	}
	for i := range subs {
		if l, ok := byID[subs[i].ID]; ok {
			subs[i].Lists = l
		} else {
			subs[i].Lists = types.JSONText("[]")
		}
	}
	return nil
}

func (s subscribers) Get(ctx context.Context, ref store.SubscriberRef) (models.Subscriber, error) {
	var out models.Subscribers
	if err := s.r.SelectContext(ctx, &out, `SELECT * FROM subscribers WHERE CASE
		WHEN ?1 > 0 THEN id = ?1 WHEN ?2 != '' THEN uuid = ?2 WHEN ?3 != '' THEN email = ?3 END`,
		ref.ID, ref.UUID, ref.Email); err != nil {
		return models.Subscriber{}, wrap(err)
	}
	if len(out) == 0 {
		return models.Subscriber{}, store.ErrNotFound
	}
	if err := s.loadLists(ctx, s.r, out); err != nil {
		return models.Subscriber{}, err
	}
	return out[0], nil
}

func (s subscribers) GetByEmails(ctx context.Context, emails []string) (models.Subscribers, error) {
	var out models.Subscribers
	if err := s.r.SelectContext(ctx, &out, `SELECT * FROM subscribers WHERE email IN (SELECT value FROM json_each(?1))`, strs(emails)); err != nil {
		return nil, wrap(err)
	}
	if err := s.loadLists(ctx, s.r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Subscriber queries (query-subscribers and friends). ?1 list IDs, ?2
// subscription status, ?3 search, ?4 subscriber status, plus the
// expression, always run in the sandbox.
const subFromWhere = `FROM subscribers
	LEFT JOIN subscriber_lists ON (
		(json_array_length(?1) > 0 OR ?2 != '')
		AND subscriber_lists.subscriber_id = subscribers.id
		AND (?2 = '' OR subscriber_lists.status = ?2)
	)
	WHERE (json_array_length(?1) = 0 OR subscriber_lists.list_id IN (SELECT value FROM json_each(?1)))
	AND (?2 = '' OR subscriber_lists.subscriber_id IS NOT NULL)
	AND (CASE WHEN ?3 != '' THEN
		instr(lower(subscribers.name), lower(?3)) > 0 OR instr(lower(subscribers.email), lower(?3)) > 0
		ELSE 1 END)
	AND (?4 = '' OR subscribers.status = ?4)
	AND ({EXPR})`

var subSortFields = map[string]string{
	"email":      "subscribers.email",
	"status":     subscriberStatusOrder,
	"name":       "subscribers.name",
	"created_at": "subscribers.created_at",
	"updated_at": "subscribers.updated_at",
}

// filterSQL fills the expression into a subscriber query, applying the
// orphan filter ("none" subscription status), and returns the
// subscription status to bind.
func filterSQL(query string, f store.SubscriberFilter) (string, string, error) {
	if err := checkExpr(f.Expr); err != nil {
		return "", "", err
	}
	cond := "1"
	if f.Expr != "" {
		cond = f.Expr
	}
	cond, subStatus := models.ApplyOrphanFilter(cond, f.SubscriptionStatus)
	return replaceAll(query, "{EXPR}", cond), subStatus, nil
}

func (s subscribers) Query(ctx context.Context, f store.SubscriberFilter, srt store.Sort, p store.Page) (models.Subscribers, int, error) {
	q, subStatus, err := filterSQL(`SELECT DISTINCT subscribers.* `+subFromWhere+` ORDER BY {ORDER} LIMIT ?6 OFFSET ?5`, f)
	if err != nil {
		return nil, 0, err
	}
	q = withOrder(q, orderBy(srt, subSortFields, "subscribers.id")+", subscribers.id")
	cq, _, _ := filterSQL(`SELECT COUNT(DISTINCT subscribers.id) `+subFromWhere, f)

	var (
		out   models.Subscribers
		total int
	)
	args := []any{ints(f.ListIDs), subStatus, f.Search, f.Status}
	if err := s.sandboxed(ctx, func(c *sqlx.Conn) error {
		if err := c.GetContext(ctx, &total, cq, args...); err != nil {
			return err
		}
		if total == 0 {
			return nil
		}
		return c.SelectContext(ctx, &out, q, append(args, p.Offset, limit(p.Limit))...)
	}); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return models.Subscribers{}, 0, nil
	}
	if err := s.loadLists(ctx, s.r, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s subscribers) Export(ctx context.Context, f store.SubscriberFilter) (func(afterID, limit int) ([]models.SubscriberExport, error), error) {
	q, subStatus, err := filterSQL(`SELECT DISTINCT subscribers.id, subscribers.uuid, subscribers.email, subscribers.name,
		subscribers.status, subscribers.attribs, subscribers.created_at, subscribers.updated_at `+subFromWhere+`
		AND subscribers.id > ?5 AND (json_array_length(?6) = 0 OR subscribers.id IN (SELECT value FROM json_each(?6)))
		ORDER BY subscribers.id LIMIT ?7`, f)
	if err != nil {
		return nil, err
	}
	args := []any{ints(f.ListIDs), subStatus, f.Search, f.Status}

	// Validate once, as upstream does before streaming.
	if err := s.sandboxed(ctx, func(c *sqlx.Conn) error {
		var out []models.SubscriberExport
		return c.SelectContext(ctx, &out, q, append(args, 0, ints(f.IDs), 0)...)
	}); err != nil {
		return nil, err
	}

	return func(afterID, lim int) ([]models.SubscriberExport, error) {
		var out []models.SubscriberExport
		err := s.sandboxed(ctx, func(c *sqlx.Conn) error {
			return c.SelectContext(ctx, &out, q, append(args, afterID, ints(f.IDs), limit(lim))...)
		})
		return out, err
	}, nil
}

// filterIDs resolves a filter to subscriber IDs in the sandbox. Writes by
// filter then act on these IDs only.
func (s *Store) filterIDs(ctx context.Context, f store.SubscriberFilter) ([]int, error) {
	q, subStatus, err := filterSQL(`SELECT DISTINCT subscribers.id `+subFromWhere, f)
	if err != nil {
		return nil, err
	}
	var ids []int
	if err := s.sandboxed(ctx, func(c *sqlx.Conn) error {
		return c.SelectContext(ctx, &ids, q, ints(f.ListIDs), subStatus, f.Search, f.Status)
	}); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s subscribers) HasLists(ctx context.Context, subIDs, listIDs []int) (map[int]bool, error) {
	res := []struct {
		SubID int  `db:"subscriber_id"`
		Has   bool `db:"has"`
	}{}
	if err := s.r.SelectContext(ctx, &res, `SELECT s.id AS subscriber_id, EXISTS (
		SELECT 1 FROM subscriber_lists sl WHERE sl.subscriber_id = s.id AND sl.list_id IN (SELECT value FROM json_each(?2))) AS has
		FROM subscribers s WHERE s.id IN (SELECT value FROM json_each(?1))`, ints(subIDs), ints(listIDs)); err != nil {
		return nil, wrap(err)
	}
	out := make(map[int]bool, len(res))
	for _, r := range res {
		out[r.SubID] = r.Has
	}
	return out, nil
}

func (s subscribers) Lists(ctx context.Context, ref store.SubscriberRef, listIDs []int, listUUIDs []string, subStatus, listType string) ([]models.List, error) {
	rows := []listRow{}
	if err := s.r.SelectContext(ctx, &rows, `SELECT lists.*, subscriber_lists.status AS subscription_status,
		subscriber_lists.created_at AS subscription_created_at, subscriber_lists.updated_at AS subscription_updated_at,
		subscriber_lists.subscriber_id
		FROM lists JOIN subscriber_lists ON (lists.id = subscriber_lists.list_id)
		WHERE subscriber_lists.subscriber_id = (SELECT id FROM subscribers WHERE CASE WHEN ?1 > 0 THEN id = ?1 ELSE uuid = ?2 END)
		AND (CASE WHEN json_array_length(?3) > 0 THEN lists.id IN (SELECT value FROM json_each(?3))
			WHEN json_array_length(?4) > 0 THEN lists.uuid IN (SELECT value FROM json_each(?4))
			ELSE 1 END)
		AND (?5 = '' OR subscriber_lists.status = ?5)
		AND (?6 = '' OR lists.optin = ?6)
		ORDER BY lists.id`, ref.ID, ref.UUID, ints(listIDs), strs(listUUIDs), subStatus, listType); err != nil {
		return nil, wrap(err)
	}
	return listModels(rows), nil
}

func (s subscribers) Profile(ctx context.Context, ref store.SubscriberRef, scope store.ListScope) (models.SubscriberExportProfile, error) {
	var out models.SubscriberExportProfile
	err := s.r.GetContext(ctx, &out, `WITH prof AS (
		SELECT id, uuid, email, name, attribs, status, created_at, updated_at FROM subscribers
		WHERE CASE WHEN ?1 > 0 THEN id = ?1 ELSE uuid = ?2 END
	)
	SELECT (SELECT email FROM prof) AS email,
		CAST(COALESCE((SELECT json_group_array(json_object('id', id, 'uuid', uuid, 'email', email, 'name', name,
			'attribs', json(attribs), 'status', status, 'created_at', `+jts("created_at")+`, 'updated_at', `+jts("updated_at")+`)) FROM prof), '{}') AS BLOB) AS profile,
		CAST(COALESCE((SELECT json_group_array(json_object('subscription_status', sl.status,
			'name', CASE WHEN l.type = 'private' THEN 'Private list' ELSE l.name END,
			'type', l.type, 'created_at', `+jts("sl.created_at")+`))
			FROM lists l JOIN subscriber_lists sl ON (sl.list_id = l.id)
			WHERE sl.subscriber_id = (SELECT id FROM prof) AND (?3 OR l.id IN (SELECT value FROM json_each(?4)))), '[]') AS BLOB) AS subscriptions,
		CAST(COALESCE((SELECT json_group_array(json_object('campaign', campaign, 'views', views)) FROM (
			SELECT c.subject AS campaign, COUNT(v.subscriber_id) AS views FROM campaign_views v
			LEFT JOIN campaigns c ON (c.id = v.campaign_id)
			WHERE v.subscriber_id = (SELECT id FROM prof)
			AND (?3 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = v.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?4))))
			GROUP BY c.id ORDER BY c.id)), '[]') AS BLOB) AS campaign_views,
		CAST(COALESCE((SELECT json_group_array(json_object('url', url, 'clicks', clicks)) FROM (
			SELECT l.url, COUNT(k.subscriber_id) AS clicks FROM link_clicks k
			LEFT JOIN links l ON (l.id = k.link_id)
			WHERE k.subscriber_id = (SELECT id FROM prof)
			AND (?3 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = k.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?4))))
			GROUP BY l.id ORDER BY l.id)), '[]') AS BLOB) AS link_clicks`,
		ref.ID, ref.UUID, scope.All, ints(scope.IDs))
	if err != nil {
		return models.SubscriberExportProfile{}, wrap(err)
	}
	return out, nil
}

// Activity: the aggregates are built in SQL; COALESCE to '[]' as upstream.
func (s subscribers) Activity(ctx context.Context, id int, scope store.ListScope) (models.SubscriberActivity, error) {
	var out models.SubscriberActivity
	err := s.r.GetContext(ctx, &out, `SELECT
		CAST(COALESCE((SELECT json_group_array(json_object('id', id, 'uuid', uuid, 'name', name, 'subject', subject,
			'view_count', view_count, 'last_viewed_at', `+jts("last_viewed_at")+`)) FROM (
			SELECT c.id, c.uuid, c.name, c.subject, COUNT(*) AS view_count, MAX(v.created_at) AS last_viewed_at
			FROM campaign_views v LEFT JOIN campaigns c ON c.id = v.campaign_id
			WHERE v.subscriber_id = ?1
			AND (?2 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = v.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?3))))
			GROUP BY c.id, c.uuid, c.name, c.subject ORDER BY last_viewed_at DESC)), '[]') AS BLOB) AS campaign_views,
		CAST(COALESCE((SELECT json_group_array(json_object('link_id', link_id, 'url', url, 'campaign_id', campaign_id,
			'campaign_uuid', campaign_uuid, 'campaign_name', campaign_name, 'campaign_subject', campaign_subject,
			'click_count', click_count, 'last_clicked_at', `+jts("last_clicked_at")+`)) FROM (
			SELECT l.id AS link_id, l.url, c.id AS campaign_id, c.uuid AS campaign_uuid, c.name AS campaign_name,
				c.subject AS campaign_subject, COUNT(*) AS click_count, MAX(k.created_at) AS last_clicked_at
			FROM link_clicks k LEFT JOIN links l ON l.id = k.link_id LEFT JOIN campaigns c ON c.id = k.campaign_id
			WHERE k.subscriber_id = ?1
			AND (?2 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = k.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?3))))
			GROUP BY l.id, l.url, c.id, c.uuid, c.name, c.subject ORDER BY last_clicked_at DESC)), '[]') AS BLOB) AS link_clicks`,
		id, scope.All, ints(scope.IDs))
	if err != nil {
		return models.SubscriberActivity{}, wrap(err)
	}
	return out, nil
}

// weeksCTE generates Monday-start weeks (Postgres' DATE_TRUNC('week'))
// from `from` (an SQLite date modifier like '-12 months') to this week.
func weeksCTE(from string) string {
	return `weeks(date) AS (
		SELECT date('now', '` + from + `', 'weekday 0', '-6 days')
		UNION ALL SELECT date(date, '+7 days') FROM weeks WHERE date < date('now', 'weekday 0', '-6 days')
	)`
}

// week truncates a timestamp column to the Monday of its week.
func week(col string) string { return "date(" + col + ", 'weekday 0', '-6 days')" }

func (s subscribers) Charts(ctx context.Context, id int, scope store.ListScope) (json.RawMessage, error) {
	var out []byte
	err := s.r.GetContext(ctx, &out, `WITH RECURSIVE `+weeksCTE("-12 months")+`,
	views AS (
		SELECT `+week("created_at")+` AS date, COUNT(*) AS count FROM campaign_views
		WHERE subscriber_id = ?1
		AND (?2 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = campaign_views.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?3))))
		AND created_at >= date('now', '-12 months') AND created_at <= ?4
		GROUP BY 1
	), clicks AS (
		SELECT `+week("created_at")+` AS date, COUNT(*) AS count FROM link_clicks
		WHERE subscriber_id = ?1
		AND (?2 OR EXISTS (SELECT 1 FROM campaign_lists cl WHERE cl.campaign_id = link_clicks.campaign_id AND cl.list_id IN (SELECT value FROM json_each(?3))))
		AND created_at >= date('now', '-12 months') AND created_at <= ?4
		GROUP BY 1
	)
	SELECT CAST(json_object(
		'campaign_views', json_group_array(json_object('date', w.date, 'count', COALESCE(v.count, 0)) ORDER BY w.date),
		'link_clicks', json_group_array(json_object('date', w.date, 'count', COALESCE(c.count, 0)) ORDER BY w.date)
	) AS BLOB)
	FROM weeks w LEFT JOIN views v ON v.date = w.date LEFT JOIN clicks c ON c.date = w.date`,
		id, scope.All, ints(scope.IDs), now())
	if err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s subscribers) DeleteActivity(ctx context.Context, id int, kind store.ActivityKind) error {
	var q string
	switch kind {
	case store.ActivityCampaignViews:
		q = `DELETE FROM campaign_views WHERE subscriber_id = ?`
	case store.ActivityLinkClicks:
		q = `DELETE FROM link_clicks WHERE subscriber_id = ?`
	default:
		return fmt.Errorf("unknown activity kind %q", kind)
	}
	_, err := s.w.ExecContext(ctx, q, id)
	return wrap(err)
}

// resolveLists returns the IDs of the lists given by ID, or, if no IDs are
// given, by UUID (upstream's listIDs CTE).
func resolveLists(ctx context.Context, tx *sqlx.Tx, ids []int, uuids []string) ([]int, error) {
	var out []int
	err := tx.SelectContext(ctx, &out, `SELECT id FROM lists WHERE CASE
		WHEN json_array_length(?1) > 0 THEN id IN (SELECT value FROM json_each(?1))
		ELSE uuid IN (SELECT value FROM json_each(?2)) END ORDER BY id`, ints(ids), strs(uuids))
	return out, wrap(err)
}

func (s subscribers) Insert(ctx context.Context, in store.SubscriberInput, lists store.ListChange) (models.Subscriber, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return models.Subscriber{}, err
	}
	var id int
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		if err := tx.GetContext(ctx, &id, `INSERT INTO subscribers (uuid, email, name, status, attribs, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, uu.String(), in.Email, in.Name, in.Status, jsonText(attribsOr(in.Attribs)), t, t); err != nil {
			return wrap(err)
		}
		listIDs, err := resolveLists(ctx, tx, lists.ListIDs, lists.ListUUIDs)
		if err != nil {
			return err
		}
		status := lists.Status
		if in.Status == models.SubscriberStatusBlockListed {
			status = models.SubscriptionStatusUnsubscribed
		}
		for _, l := range listIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO subscriber_lists (subscriber_id, list_id, status, created_at, updated_at)
				VALUES (?1, ?2, ?3, ?4, ?4) ON CONFLICT (subscriber_id, list_id) DO UPDATE SET updated_at = ?4, status = ?3`,
				id, l, status, t); err != nil {
				return wrap(err)
			}
		}
		return nil
	})
	if err != nil {
		return models.Subscriber{}, s.emailConflict(ctx, err, in.Email)
	}
	return s.Get(ctx, store.SubscriberRef{ID: id})
}

// emailConflict mirrors Postgres: an exact duplicate e-mail is a conflict
// on "email" (subscribers_email_key); one differing only in case is a
// conflict on the idx_subs_email index. SQLite checks the expression index
// first, so tell the two apart here.
func (s *Store) emailConflict(ctx context.Context, err error, email string) error {
	var ce *store.ConflictError
	if email == "" || !asConflict(err, &ce) || ce.Field != "idx_subs_email" {
		return err
	}
	var n int
	if s.r.GetContext(ctx, &n, `SELECT COUNT(*) FROM subscribers WHERE email = ?`, email) == nil && n > 0 {
		ce.Field = "email"
	}
	return err
}

func attribsOr(a models.JSON) models.JSON {
	if a == nil {
		return models.JSON{}
	}
	return a
}

// updateSubscriber is update-subscriber: empty fields keep their value,
// attribs are replaced.
func updateSubscriber(ctx context.Context, tx *sqlx.Tx, id int, in store.SubscriberInput, t string) (int64, error) {
	res, err := tx.ExecContext(ctx, `UPDATE subscribers SET
		email = (CASE WHEN ?2 != '' THEN ?2 ELSE email END),
		name = (CASE WHEN ?3 != '' THEN ?3 ELSE name END),
		status = (CASE WHEN ?4 != '' THEN ?4 ELSE status END),
		attribs = ?5,
		updated_at = ?6
		WHERE id = ?1`, id, in.Email, in.Name, in.Status, jsonText(attribsOr(in.Attribs)), t)
	if err != nil {
		return 0, wrap(err)
	}
	return res.RowsAffected()
}

func (s subscribers) Update(ctx context.Context, id int, in store.SubscriberInput) (models.Subscriber, error) {
	if err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		_, err := updateSubscriber(ctx, tx, id, in, now())
		return err
	}); err != nil {
		return models.Subscriber{}, s.emailConflict(ctx, err, in.Email)
	}
	return s.Get(ctx, store.SubscriberRef{ID: id})
}

func (s subscribers) UpdateWithLists(ctx context.Context, id int, in store.SubscriberInput, lists store.ListChange) (models.Subscriber, error) {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		n, err := updateSubscriber(ctx, tx, id, in, t)
		if err != nil || n == 0 {
			return err
		}
		listIDs, err := resolveLists(ctx, tx, lists.ListIDs, lists.ListUUIDs)
		if err != nil {
			return err
		}
		if lists.ReplaceExisting {
			if _, err := tx.ExecContext(ctx, `DELETE FROM subscriber_lists WHERE subscriber_id = ?1
				AND list_id NOT IN (SELECT value FROM json_each(?2))
				AND (?3 OR list_id IN (SELECT value FROM json_each(?4)))`,
				id, ints(listIDs), lists.Permitted.All, ints(lists.Permitted.IDs)); err != nil {
				return wrap(err)
			}
		}
		for _, l := range listIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO subscriber_lists (subscriber_id, list_id, status, created_at, updated_at)
				VALUES (?1, ?2, CASE WHEN ?3 = 'blocklisted' THEN 'unsubscribed' ELSE ?4 END, ?6, ?6)
				ON CONFLICT (subscriber_id, list_id) DO UPDATE SET status = (CASE
					WHEN ?3 = 'blocklisted' THEN 'unsubscribed'
					WHEN subscriber_lists.status = 'confirmed' THEN 'confirmed'
					WHEN ?5 THEN ?4
					WHEN subscriber_lists.status = 'unsubscribed' THEN 'unsubscribed'
					ELSE ?4 END)`,
				id, l, in.Status, lists.Status, lists.AllowResubscribe, t); err != nil {
				return wrap(err)
			}
		}
		return nil
	})
	if err != nil {
		return models.Subscriber{}, s.emailConflict(ctx, err, in.Email)
	}
	return s.Get(ctx, store.SubscriberRef{ID: id})
}

func (s subscribers) Import(ctx context.Context, rows []store.SubscriberInput, o store.ImportOptions) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		for _, r := range rows {
			uu, err := uuid.NewV4()
			if err != nil {
				return err
			}
			var sub struct {
				ID     int    `db:"id"`
				Status string `db:"status"`
			}
			if o.Mode == store.ImportBlocklist {
				if err := tx.GetContext(ctx, &sub, `INSERT INTO subscribers (uuid, email, name, attribs, status, created_at, updated_at)
					VALUES (?1, ?2, ?3, ?4, 'blocklisted', ?5, ?5)
					ON CONFLICT (email) DO UPDATE SET status = 'blocklisted', updated_at = ?5 RETURNING id, status`,
					uu.String(), r.Email, r.Name, jsonText(attribsOr(r.Attribs)), t); err != nil {
					return wrap(err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'unsubscribed', updated_at = ?2 WHERE subscriber_id = ?1`, sub.ID, t); err != nil {
					return wrap(err)
				}
				continue
			}

			if err := tx.GetContext(ctx, &sub, `INSERT INTO subscribers (uuid, email, name, attribs, status, created_at, updated_at)
				VALUES (?1, ?2, ?3, ?4, 'enabled', ?6, ?6)
				ON CONFLICT (email) DO UPDATE SET
					name = (CASE WHEN ?5 THEN ?3 ELSE subscribers.name END),
					attribs = (CASE WHEN ?5 THEN ?4 ELSE subscribers.attribs END),
					updated_at = ?6
				RETURNING id, status`, uu.String(), r.Email, r.Name, jsonText(attribsOr(r.Attribs)), o.OverwriteUserInfo, t); err != nil {
				return wrap(err)
			}
			status := o.SubscriptionStatus
			if sub.Status == models.SubscriberStatusBlockListed {
				status = models.SubscriptionStatusUnsubscribed
			}
			for _, l := range o.ListIDs {
				if _, err := tx.ExecContext(ctx, `INSERT INTO subscriber_lists (subscriber_id, list_id, status, created_at, updated_at)
					VALUES (?1, ?2, ?3, ?5, ?5)
					ON CONFLICT (subscriber_id, list_id) DO UPDATE SET updated_at = ?5,
						status = CASE WHEN ?4 THEN excluded.status ELSE subscriber_lists.status END`,
					sub.ID, l, status, o.OverwriteSubStatus, t); err != nil {
					return wrap(err)
				}
			}
		}
		return nil
	})
}

func blocklistIDs(ctx context.Context, tx *sqlx.Tx, ids []int) error {
	t := now()
	if _, err := tx.ExecContext(ctx, `UPDATE subscribers SET status = 'blocklisted', updated_at = ?2 WHERE id IN (SELECT value FROM json_each(?1))`, ints(ids), t); err != nil {
		return wrap(err)
	}
	_, err := tx.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'unsubscribed', updated_at = ?2 WHERE subscriber_id IN (SELECT value FROM json_each(?1))`, ints(ids), t)
	return wrap(err)
}

func (s subscribers) Blocklist(ctx context.Context, ids []int) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error { return blocklistIDs(ctx, tx, ids) })
}

func (s subscribers) BlocklistByFilter(ctx context.Context, f store.SubscriberFilter) (int, error) {
	ids, err := s.filterIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), s.inTx(ctx, func(tx *sqlx.Tx) error { return blocklistIDs(ctx, tx, ids) })
}

func (s subscribers) Delete(ctx context.Context, ids []int, uuids []string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM subscribers WHERE CASE WHEN json_array_length(?1) > 0
		THEN id IN (SELECT value FROM json_each(?1)) ELSE uuid IN (SELECT value FROM json_each(?2)) END`, ints(ids), strs(uuids))
	return wrap(err)
}

func (s subscribers) DeleteByFilter(ctx context.Context, f store.SubscriberFilter) (int, error) {
	ids, err := s.filterIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if _, err := s.w.ExecContext(ctx, `DELETE FROM subscribers WHERE id IN (SELECT value FROM json_each(?1))`, ints(ids)); err != nil {
		return 0, wrap(err)
	}
	return len(ids), nil
}

func (s subscribers) DeleteOrphans(ctx context.Context) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM subscribers WHERE NOT EXISTS (SELECT 1 FROM subscriber_lists b WHERE b.subscriber_id = subscribers.id)`)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s subscribers) DeleteBlocklisted(ctx context.Context) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM subscribers WHERE status = 'blocklisted'`)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s subscribers) UnsubscribeByCampaign(ctx context.Context, subUUID, campUUID string, blocklist, allUnconfirmed bool) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		var id int
		if err := tx.GetContext(ctx, &id, `UPDATE subscribers SET status = (CASE WHEN ?2 THEN 'blocklisted' ELSE status END)
			WHERE uuid = ?1 RETURNING id`, subUUID, blocklist); err != nil {
			return notFoundOK(err)
		}
		_, err := tx.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'unsubscribed', updated_at = ?5
			WHERE subscriber_id = ?1 AND status != 'unsubscribed' AND CASE
				WHEN ?3 THEN 1
				WHEN ?4 THEN status = 'unconfirmed'
				ELSE list_id IN (SELECT cl.list_id FROM campaign_lists cl JOIN campaigns c ON (c.id = cl.campaign_id) WHERE c.uuid = ?2)
			END`, id, campUUID, blocklist, allUnconfirmed, t)
		return wrap(err)
	})
}

// notFoundOK turns "no rows" into nil (a no-op), other errors into store errors.
func notFoundOK(err error) error {
	if err == nil || notFound(err) == store.ErrNotFound {
		return nil
	}
	return wrap(err)
}

// Subscriptions.

type subscriptionRow struct {
	models.Subscription
	Tags jsonStrings `db:"tags"`
}

func (s subscriptions) List(ctx context.Context, ref store.SubscriberRef, allLists bool) ([]models.Subscription, error) {
	var rows []subscriptionRow
	if err := s.r.SelectContext(ctx, &rows, `WITH sub AS (
		SELECT id FROM subscribers WHERE CASE WHEN ?1 > 0 THEN id = ?1 ELSE uuid = ?2 END
	)
	SELECT lists.*, subscriber_lists.status AS subscription_status, subscriber_lists.created_at AS subscription_created_at,
		subscriber_lists.meta AS subscription_meta
	FROM lists LEFT JOIN subscriber_lists ON (subscriber_lists.list_id = lists.id AND subscriber_lists.subscriber_id = (SELECT id FROM sub))
	WHERE CASE WHEN ?3 THEN 1 ELSE subscriber_lists.status IS NOT NULL END
	ORDER BY `+subscriptionStatusOrder+` NULLS LAST`, ref.ID, ref.UUID, allLists); err != nil {
		return nil, wrap(err)
	}
	if rows == nil {
		return nil, nil
	}
	out := make([]models.Subscription, len(rows))
	for i, r := range rows {
		out[i] = r.Subscription
		out[i].Tags = []string(r.Tags)
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
	return out, nil
}

func addSubscriptions(ctx context.Context, tx *sqlx.Tx, subIDs, listIDs []int, status string, overwrite bool) error {
	t := now()
	for _, sid := range subIDs {
		for _, l := range listIDs {
			q := `INSERT INTO subscriber_lists (subscriber_id, list_id, status, created_at, updated_at)
				VALUES (?1, ?2, CASE WHEN ?3 != '' THEN ?3 ELSE 'unconfirmed' END, ?4, ?4)
				ON CONFLICT (subscriber_id, list_id) DO UPDATE SET status = (CASE WHEN ?3 != '' THEN ?3 ELSE subscriber_lists.status END)`
			if !overwrite {
				q = `INSERT INTO subscriber_lists (subscriber_id, list_id, status, created_at, updated_at)
				VALUES (?1, ?2, CASE WHEN ?3 != '' THEN ?3 ELSE 'unconfirmed' END, ?4, ?4)
				ON CONFLICT (subscriber_id, list_id) DO NOTHING`
			}
			if _, err := tx.ExecContext(ctx, q, sid, l, status, t); err != nil {
				return wrap(err)
			}
		}
	}
	return nil
}

func (s subscriptions) Add(ctx context.Context, subIDs, listIDs []int, status string) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error { return addSubscriptions(ctx, tx, subIDs, listIDs, status, true) })
}

func (s subscriptions) AddByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int, status string) (int, error) {
	ids, err := s.filterIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), s.inTx(ctx, func(tx *sqlx.Tx) error { return addSubscriptions(ctx, tx, ids, listIDs, status, false) })
}

func (s subscriptions) Remove(ctx context.Context, subIDs, listIDs []int) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM subscriber_lists WHERE subscriber_id IN (SELECT value FROM json_each(?1))
		AND list_id IN (SELECT value FROM json_each(?2))`, ints(subIDs), ints(listIDs))
	return wrap(err)
}

func (s subscriptions) RemoveByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int) (int, error) {
	ids, err := s.filterIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), s.Remove(ctx, ids, listIDs)
}

func (s subscriptions) Unsubscribe(ctx context.Context, subIDs, listIDs []int, listUUIDs []string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'unsubscribed', updated_at = ?4
		WHERE subscriber_id IN (SELECT value FROM json_each(?1))
		AND list_id IN (SELECT id FROM lists WHERE CASE WHEN json_array_length(?2) > 0
			THEN id IN (SELECT value FROM json_each(?2)) ELSE uuid IN (SELECT value FROM json_each(?3)) END)`,
		ints(subIDs), ints(listIDs), strs(listUUIDs), now())
	return wrap(err)
}

func (s subscriptions) UnsubscribeByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int) (int, error) {
	ids, err := s.filterIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), s.Unsubscribe(ctx, ids, listIDs, nil)
}

func (s subscriptions) ConfirmOptin(ctx context.Context, subUUID string, listUUIDs []string, meta models.JSON) error {
	if meta == nil {
		meta = models.JSON{}
	}
	_, err := s.w.ExecContext(ctx, `UPDATE subscriber_lists SET status = 'confirmed', meta = json_patch(meta, ?3), updated_at = ?4
		WHERE subscriber_id = (SELECT id FROM subscribers WHERE uuid = ?1)
		AND list_id IN (SELECT id FROM lists WHERE uuid IN (SELECT value FROM json_each(?2)))`,
		subUUID, strs(listUUIDs), jsonText(meta), now())
	return wrap(err)
}

func (s subscriptions) DeleteUnconfirmed(ctx context.Context, before time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM subscriber_lists WHERE status = 'unconfirmed'
		AND list_id IN (SELECT id FROM lists WHERE optin = 'double') AND created_at < ?1`, ts(before))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
