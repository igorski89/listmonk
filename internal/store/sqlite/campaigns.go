package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

type campaigns struct{ *Store }

type campaignRow struct {
	models.Campaign
	Tags        jsonStrings    `db:"tags"`
	MediaIDs    jsonInt64s     `db:"media_id"`
	ArchiveMeta types.JSONText `db:"archive_meta"`
}

func (r campaignRow) model() models.Campaign {
	c := r.Campaign
	c.Tags = []string(r.Tags)
	c.MediaIDs = []int64(r.MediaIDs)
	c.ArchiveMeta = []byte(r.ArchiveMeta)
	return c
}

func campaignModels(rows []campaignRow) models.Campaigns {
	if rows == nil {
		return nil
	}
	out := make(models.Campaigns, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out
}

// tsOrNull binds a nullable time in UTC.
func tsOrNull(t null.Time) any {
	if !t.Valid {
		return nil
	}
	return ts(t.Time)
}

// campListsJSON is a campaign's lists as JSON (query-campaigns).
const campListsJSON = `CAST(COALESCE((SELECT json_group_array(json_object('id', COALESCE(cl.list_id, 0), 'name', cl.list_name))
	FROM campaign_lists cl WHERE cl.campaign_id = c.id), '[]') AS BLOB)`

var campSortFields = map[string]string{
	"name":       "c.name",
	"status":     `CASE c.status WHEN 'draft' THEN 0 WHEN 'running' THEN 1 WHEN 'scheduled' THEN 2 WHEN 'paused' THEN 3 WHEN 'cancelled' THEN 4 WHEN 'finished' THEN 5 END`,
	"created_at": "c.created_at",
	"updated_at": "c.updated_at",
	"started_at": "c.started_at",
	"send_at":    "c.send_at",
}

// loadStats attaches lists, media, views, clicks and bounces
// (get-campaign-stats) and replaces null tags.
func (s campaigns) loadStats(ctx context.Context, out models.Campaigns) error {
	for i := range out {
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	var meta []models.CampaignMeta
	if err := s.r.SelectContext(ctx, &meta, `SELECT c.value AS campaign_id,
		(SELECT COUNT(*) FROM campaign_views WHERE campaign_id = c.value) AS views,
		(SELECT COUNT(*) FROM link_clicks WHERE campaign_id = c.value) AS clicks,
		(SELECT COUNT(*) FROM bounces WHERE campaign_id = c.value) AS bounces,
		CAST(COALESCE((SELECT json_group_array(json_object('id', list_id, 'name', list_name)) FROM campaign_lists WHERE campaign_id = c.value), '[]') AS BLOB) AS lists,
		CAST(COALESCE((SELECT json_group_array(json_object('id', media_id, 'filename', filename)) FROM campaign_media WHERE campaign_id = c.value), '[]') AS BLOB) AS media
		FROM json_each(?1) c ORDER BY c.key`, ints(out.GetIDs())); err != nil {
		return wrap(err)
	}
	for i, m := range meta {
		if i < len(out) && m.CampaignID == out[i].ID {
			out[i].Lists = m.Lists
			out[i].Views = m.Views
			out[i].Clicks = m.Clicks
			out[i].Bounces = m.Bounces
			out[i].Media = m.Media
		}
	}
	return nil
}

func (s campaigns) Query(ctx context.Context, f store.CampaignFilter, srt store.Sort, p store.Page) (models.Campaigns, int, error) {
	var rows []campaignRow
	q := withOrder(`SELECT c.*, COUNT(*) OVER () AS total, `+campListsJSON+` AS lists FROM campaigns c
		WHERE (json_array_length(?1) = 0 OR c.status IN (SELECT value FROM json_each(?1)))
		AND (json_array_length(?2) = 0 OR NOT EXISTS (SELECT 1 FROM json_each(?2) t WHERE t.value NOT IN (SELECT value FROM json_each(c.tags))))
		AND (?3 = '' OR (c.name || ' ' || c.subject) LIKE '%' || ?3 || '%')
		AND (?4 = '' OR c.type = ?4)
		AND (?5 OR EXISTS (SELECT 1 FROM campaign_lists WHERE campaign_id = c.id AND list_id IN (SELECT value FROM json_each(?6))))
		ORDER BY {ORDER} LIMIT ?8 OFFSET ?7`, orderBy(srt, campSortFields, "c.created_at")+", c.id")
	if err := s.r.SelectContext(ctx, &rows, q, strs(f.Statuses), strs(f.Tags), f.Search, f.Type, f.Scope.All, ints(f.Scope.IDs),
		p.Offset, limit(p.Limit)); err != nil {
		return nil, 0, wrap(err)
	}
	out := campaignModels(rows)
	if err := s.loadStats(ctx, out); err != nil {
		return nil, 0, err
	}
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s campaigns) Get(ctx context.Context, ref store.CampaignRef, tpl store.TemplateKind) (models.Campaign, error) {
	var rows []campaignRow
	if err := s.r.SelectContext(ctx, &rows, `SELECT c.*,
		COALESCE(t.body, (SELECT body FROM templates WHERE is_default = true LIMIT 1), '') AS template_body
		FROM campaigns c LEFT JOIN templates t ON (CASE WHEN ?4 = 'default' THEN t.id = c.template_id ELSE t.id = c.archive_template_id END)
		WHERE CASE WHEN ?1 > 0 THEN c.id = ?1 WHEN ?3 != '' THEN c.archive_slug = ?3 ELSE c.uuid = ?2 END`,
		ref.ID, ref.UUID, ref.ArchiveSlug, string(tpl)); err != nil {
		return models.Campaign{}, wrap(err)
	}
	if len(rows) == 0 {
		return models.Campaign{}, store.ErrNotFound
	}
	out := campaignModels(rows)
	if err := s.loadStats(ctx, out); err != nil {
		return models.Campaign{}, err
	}
	return out[0], nil
}

func (s campaigns) GetForPreview(ctx context.Context, id, tplID int) (models.Campaign, error) {
	var row campaignRow
	if err := s.r.GetContext(ctx, &row, `SELECT c.*, COALESCE(t.body, '') AS template_body, `+campListsJSON+` AS lists
		FROM campaigns c LEFT JOIN templates t ON (t.id = (CASE WHEN ?2 = 0 THEN c.template_id ELSE ?2 END))
		WHERE c.id = ?1`, id, tplID); err != nil {
		return models.Campaign{}, notFound(err)
	}
	return row.model(), nil
}

func (s campaigns) Archived(ctx context.Context, listUUID string, p store.Page) (models.Campaigns, int, error) {
	var rows []campaignRow
	if err := s.r.SelectContext(ctx, &rows, `SELECT COUNT(*) OVER () AS total, c.*,
		COALESCE(t.body, (SELECT body FROM templates WHERE is_default = true LIMIT 1), '') AS template_body
		FROM campaigns c LEFT JOIN templates t ON (t.id = c.archive_template_id)
		WHERE c.archive = true AND c.type = 'regular' AND c.status IN ('running', 'paused', 'finished')
		AND (?3 = '' OR EXISTS (SELECT 1 FROM campaign_lists cl JOIN lists l ON l.id = cl.list_id
			WHERE cl.campaign_id = c.id AND l.uuid = ?3 AND l.type = 'public'))
		ORDER BY c.started_at DESC NULLS LAST, c.id DESC LIMIT ?2 OFFSET ?1`, p.Offset, limit(p.Limit), listUUID); err != nil {
		return models.Campaigns{}, 0, wrap(err)
	}
	out := campaignModels(rows)
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

// setCampaignLists replaces a campaign's lists and media.
func setCampaignLists(ctx context.Context, tx *sqlx.Tx, id int, listIDs, mediaIDs []int, replace bool) error {
	if replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM campaign_lists WHERE campaign_id = ?1 AND (list_id IS NULL OR list_id NOT IN (SELECT value FROM json_each(?2)))`, id, ints(listIDs)); err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM campaign_media WHERE campaign_id = ?1 AND (media_id IS NULL OR media_id NOT IN (SELECT value FROM json_each(?2)))`, id, ints(mediaIDs)); err != nil {
			return wrap(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO campaign_media (campaign_id, media_id, filename)
		SELECT ?1, id, filename FROM media WHERE id IN (SELECT value FROM json_each(?2))
		ON CONFLICT (campaign_id, media_id) DO NOTHING`, id, ints(mediaIDs)); err != nil {
		return wrap(err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO campaign_lists (campaign_id, list_id, list_name)
		SELECT ?1, id, name FROM lists WHERE id IN (SELECT value FROM json_each(?2))
		ON CONFLICT (campaign_id, list_id) DO UPDATE SET list_name = excluded.list_name`, id, ints(listIDs))
	return wrap(err)
}

func (s campaigns) Create(ctx context.Context, o models.Campaign, listIDs, mediaIDs []int) (models.Campaign, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return models.Campaign{}, err
	}
	var id int
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		// The template: the given one, else the default unless visual. A
		// visual template's body and source are copied into the campaign
		// and no template_id is kept.
		var tpl struct {
			ID         sql.NullInt64  `db:"id"`
			Body       string         `db:"body"`
			BodySource sql.NullString `db:"body_source"`
		}
		err := tx.GetContext(ctx, &tpl, `SELECT
			(CASE WHEN type = 'campaign_visual' THEN NULL ELSE id END) AS id,
			(CASE WHEN type = 'campaign_visual' THEN body ELSE '' END) AS body,
			(CASE WHEN type = 'campaign_visual' THEN body_source ELSE NULL END) AS body_source
			FROM templates WHERE CASE WHEN ?1 IS NOT NULL THEN id = ?1 ELSE ?2 != 'visual' AND is_default = true END LIMIT 1`,
			o.TemplateID, o.ContentType)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return wrap(err)
		}

		body := o.Body
		if body == "" {
			body = tpl.Body
		}
		bodySource := o.BodySource
		if !bodySource.Valid && tpl.BodySource.Valid {
			bodySource = null.StringFrom(tpl.BodySource.String)
		}
		archiveMeta := string(o.ArchiveMeta)
		if archiveMeta == "" {
			archiveMeta = "{}"
		}
		t := now()
		if err := tx.GetContext(ctx, &id, `INSERT INTO campaigns (uuid, type, name, subject, from_email, body, altbody,
			content_type, send_at, headers, attribs, tags, messenger, template_id, to_send, max_subscriber_id,
			archive, archive_slug, archive_template_id, archive_meta, body_source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			uu.String(), o.Type, o.Name, o.Subject, o.FromEmail, body, o.AltBody, o.ContentType, tsOrNull(o.SendAt),
			jsonText(headersOr(o.Headers)), jsonText(attribsOr(o.Attribs)), strs(o.Tags), o.Messenger, tpl.ID,
			o.Archive, o.ArchiveSlug, o.ArchiveTemplateID, archiveMeta, bodySource, t, t); err != nil {
			return wrap(err)
		}
		return setCampaignLists(ctx, tx, id, listIDs, mediaIDs, false)
	})
	if err != nil {
		return models.Campaign{}, err
	}
	return s.Get(ctx, store.CampaignRef{ID: id}, store.TemplateCampaign)
}

func headersOr(h models.Headers) models.Headers {
	if h == nil {
		return models.Headers{}
	}
	return h
}

func (s campaigns) Update(ctx context.Context, id int, o models.Campaign, listIDs, mediaIDs []int) (models.Campaign, error) {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		archiveMeta := string(o.ArchiveMeta)
		if archiveMeta == "" {
			archiveMeta = "{}"
		}
		res, err := tx.ExecContext(ctx, `UPDATE campaigns SET
			name = ?2, subject = ?3, from_email = ?4, body = ?5,
			altbody = (CASE WHEN ?6 = '' THEN NULL ELSE ?6 END),
			content_type = ?7, send_at = ?8,
			status = (CASE WHEN status = 'scheduled' AND ?8 IS NULL THEN 'draft' ELSE status END),
			headers = ?9, attribs = ?10, tags = ?11, messenger = ?12,
			template_id = (CASE WHEN ?7 = 'visual' THEN NULL ELSE ?13 END),
			archive = ?14, archive_slug = ?15,
			archive_template_id = (CASE WHEN ?7 = 'visual' THEN NULL ELSE ?16 END),
			archive_meta = ?17, body_source = ?18, updated_at = ?19
			WHERE id = ?1`, id, o.Name, o.Subject, o.FromEmail, o.Body, o.AltBody, o.ContentType, tsOrNull(o.SendAt),
			jsonText(headersOr(o.Headers)), jsonText(attribsOr(o.Attribs)), strs(o.Tags), o.Messenger, o.TemplateID,
			o.Archive, o.ArchiveSlug, o.ArchiveTemplateID, archiveMeta, o.BodySource, now())
		if err != nil {
			return wrap(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return setCampaignLists(ctx, tx, id, listIDs, mediaIDs, true)
	})
	if err != nil {
		return models.Campaign{}, err
	}
	return s.Get(ctx, store.CampaignRef{ID: id}, store.TemplateCampaign)
}

func (s campaigns) SetStatus(ctx context.Context, id int, status string) error {
	// As upstream: starting a campaign that has a send time schedules it.
	res, err := s.w.ExecContext(ctx, `UPDATE campaigns SET
		status = (CASE WHEN send_at IS NOT NULL AND ?2 = 'running' THEN 'scheduled' ELSE ?2 END),
		updated_at = ?3 WHERE id = ?1`, id, status, now())
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s campaigns) SetArchive(ctx context.Context, id int, a store.ArchiveSettings) error {
	meta := ""
	if a.Meta != nil {
		meta = jsonText(a.Meta)
	}
	_, err := s.w.ExecContext(ctx, `UPDATE campaigns SET
		archive = ?2,
		archive_slug = (CASE WHEN ?3 = '' THEN NULL ELSE ?3 END),
		archive_template_id = (CASE WHEN ?4 > 0 THEN ?4 ELSE archive_template_id END),
		archive_meta = (CASE WHEN ?5 != '' THEN json(?5) ELSE archive_meta END),
		updated_at = ?6
		WHERE id = ?1`, id, a.Enabled, a.Slug, a.TemplateID, meta, now())
	return wrap(err)
}

func (s campaigns) Delete(ctx context.Context, id int) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM campaigns WHERE id = ?`, id)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s campaigns) DeleteMany(ctx context.Context, f store.CampaignFilter) (int, error) {
	search := f.Search
	if len(f.IDs) > 0 {
		search = ""
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM campaigns WHERE (CASE
		WHEN json_array_length(?1) > 0 THEN id IN (SELECT value FROM json_each(?1))
		ELSE (?2 = '' OR (name || ' ' || subject) LIKE '%' || ?2 || '%')
			AND (json_array_length(?5) = 0 OR status IN (SELECT value FROM json_each(?5)))
			AND (json_array_length(?6) = 0 OR NOT EXISTS (SELECT 1 FROM json_each(?6) t WHERE t.value NOT IN (SELECT value FROM json_each(campaigns.tags))))
			AND (?7 = '' OR type = ?7)
		END)
		AND (?3 OR EXISTS (SELECT 1 FROM campaign_lists WHERE campaign_id = campaigns.id AND list_id IN (SELECT value FROM json_each(?4))))`,
		ints(f.IDs), search, f.Scope.All, ints(f.Scope.IDs), strs(f.Statuses), strs(f.Tags), f.Type)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s campaigns) HasLists(ctx context.Context, id int, listIDs []int) (bool, error) {
	var has bool
	err := s.r.GetContext(ctx, &has, `SELECT EXISTS (SELECT 1 FROM campaign_lists WHERE campaign_id = ?1
		AND list_id IN (SELECT value FROM json_each(?2)))`, id, ints(listIDs))
	return has, wrap(err)
}

func (s campaigns) RunningStats(ctx context.Context, scope store.ListScope) ([]models.CampaignStats, error) {
	out := []models.CampaignStats{}
	if err := s.r.SelectContext(ctx, &out, `SELECT id, status, to_send, sent, started_at, updated_at FROM campaigns
		WHERE status = 'running' AND (?1 OR EXISTS (SELECT 1 FROM campaign_lists WHERE campaign_id = campaigns.id
			AND list_id IN (SELECT value FROM json_each(?2))))`, scope.All, ints(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// recipientsWhere selects a campaign's recipients among subscriber_lists
// sl (joined to its campaign's lists cl and subscribers s) for a campaign
// of type ?type: double opt-in lists need confirmed subscriptions (opt-in
// campaigns: unconfirmed), single opt-in lists anything but unsubscribed;
// never blocklisted subscribers.
func recipientsWhere(typ string) string {
	return `s.status != 'blocklisted' AND (CASE
		WHEN ` + typ + ` = 'optin' THEN sl.status = 'unconfirmed' AND l.optin = 'double'
		WHEN l.optin = 'double' THEN sl.status = 'confirmed'
		ELSE sl.status != 'unsubscribed' END)`
}

func (s campaigns) Next(ctx context.Context, inProgress []store.CampaignProgress) ([]*models.Campaign, error) {
	var out []*models.Campaign
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		ids := make([]int, 0, len(inProgress))
		for _, p := range inProgress {
			ids = append(ids, p.ID)
			if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET sent = sent + ?2, last_subscriber_id = MAX(last_subscriber_id, ?3)
				WHERE id = ?1`, p.ID, p.Sent, p.LastSubID); err != nil {
				return wrap(err)
			}
		}

		var rows []campaignRow
		if err := tx.SelectContext(ctx, &rows, `SELECT c.*,
			COALESCE(t.body, (SELECT body FROM templates WHERE is_default = true LIMIT 1), '') AS template_body,
			(SELECT json_group_array(media_id) FROM campaign_media WHERE campaign_id = c.id AND media_id IS NOT NULL) AS media_id
			FROM campaigns c LEFT JOIN templates t ON (t.id = c.template_id)
			WHERE (c.status = 'running' OR (c.status = 'scheduled' AND ?1 >= c.send_at))
			AND c.id NOT IN (SELECT value FROM json_each(?2))`, t, ints(ids)); err != nil {
			return wrap(err)
		}

		for _, r := range rows {
			// Recipients and the highest subscriber ID, for sending in ID
			// order up to it. Campaigns without recipients are left as is.
			var cnt struct {
				ToSend int `db:"to_send"`
				MaxID  int `db:"max_id"`
			}
			if err := tx.GetContext(ctx, &cnt, `SELECT COUNT(DISTINCT sl.subscriber_id) AS to_send, COALESCE(MAX(sl.subscriber_id), 0) AS max_id
				FROM campaign_lists cl JOIN lists l ON (l.id = cl.list_id)
				JOIN subscriber_lists sl ON (sl.list_id = l.id)
				JOIN subscribers s ON (s.id = sl.subscriber_id)
				WHERE cl.campaign_id = ?1 AND `+recipientsWhere("?2"), r.ID, r.Type); err != nil {
				return wrap(err)
			}
			if cnt.ToSend > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET to_send = ?2, status = 'running', max_subscriber_id = ?3,
					started_at = COALESCE(started_at, ?4) WHERE id = ?1`, r.ID, cnt.ToSend, cnt.MaxID, t); err != nil {
					return wrap(err)
				}
			}
			c := r.model()
			if len(c.MediaIDs) == 0 {
				c.MediaIDs = nil
			}
			out = append(out, &c)
		}
		return nil
	})
	return out, err
}

func (s campaigns) NextSubscribers(ctx context.Context, campID, afterID, lim int) ([]models.Subscriber, error) {
	var camp struct {
		Type  string `db:"type"`
		MaxID int    `db:"max_subscriber_id"`
	}
	if err := s.r.GetContext(ctx, &camp, `SELECT type, max_subscriber_id FROM campaigns WHERE id = ? AND status = 'running'`, campID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap(err)
	}
	var out []models.Subscriber
	if err := s.r.SelectContext(ctx, &out, `SELECT * FROM subscribers WHERE id IN (
		SELECT DISTINCT s.id FROM subscriber_lists sl
		JOIN campaign_lists cl ON (cl.list_id = sl.list_id AND cl.campaign_id = ?1)
		JOIN lists l ON (l.id = sl.list_id)
		JOIN subscribers s ON (s.id = sl.subscriber_id)
		WHERE s.id > ?3 AND s.id <= ?4 AND `+recipientsWhere("?2")+`
		ORDER BY s.id LIMIT ?5) ORDER BY id`, campID, camp.Type, afterID, camp.MaxID, lim); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s campaigns) SetCounts(ctx context.Context, id, toSend, sent, lastSubID int) error {
	_, err := s.w.ExecContext(ctx, `UPDATE campaigns SET
		to_send = (CASE WHEN ?2 != 0 THEN ?2 ELSE to_send END),
		sent = sent + ?3, last_subscriber_id = MAX(last_subscriber_id, ?4), updated_at = ?5
		WHERE id = ?1`, id, toSend, sent, lastSubID, now())
	return wrap(err)
}
