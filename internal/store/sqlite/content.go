package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/media"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

type templates struct{ *Store }
type mediaStore struct{ *Store }
type tracking struct{ *Store }

// Templates.

var templateSortFields = map[string]string{
	"name":       "name",
	"type":       `CASE type WHEN 'campaign' THEN 0 WHEN 'campaign_visual' THEN 1 WHEN 'tx' THEN 2 END`,
	"created_at": "created_at",
	"updated_at": "updated_at",
}

const templatesSQL = `SELECT id, name, type, subject,
	(CASE WHEN ?2 = 0 THEN body ELSE '' END) AS body,
	(CASE WHEN ?2 = 0 THEN body_source ELSE NULL END) AS body_source,
	is_default, created_at, updated_at
	FROM templates WHERE (?1 = 0 OR id = ?1) AND (?3 = '' OR type = ?3)
	AND (?4 = '' OR name LIKE '%' || ?4 || '%')
	ORDER BY {ORDER}`

func (s templates) Query(ctx context.Context, search, typ string, noBody bool, srt store.Sort) ([]models.Template, error) {
	out := []models.Template{}
	q := withOrder(templatesSQL, orderBy(srt, templateSortFields, "created_at")+", id")
	if err := s.r.SelectContext(ctx, &out, q, 0, noBody, typ, search); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s templates) Get(ctx context.Context, id int, noBody bool) (models.Template, error) {
	var out []models.Template
	if err := s.r.SelectContext(ctx, &out, withOrder(templatesSQL, "created_at"), id, noBody, "", ""); err != nil {
		return models.Template{}, wrap(err)
	}
	if len(out) == 0 {
		return models.Template{}, store.ErrNotFound
	}
	return out[0], nil
}

func (s templates) Create(ctx context.Context, t store.TemplateInput) (models.Template, error) {
	var id int
	n := now()
	if err := s.w.GetContext(ctx, &id, `INSERT INTO templates (name, type, subject, body, body_source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, t.Name, t.Type, t.Subject, t.Body, t.BodySource, n, n); err != nil {
		return models.Template{}, wrap(err)
	}
	return s.Get(ctx, id, false)
}

func (s templates) Update(ctx context.Context, id int, t store.TemplateInput) (models.Template, error) {
	// As upstream: an empty subject falls back to the template's name.
	res, err := s.w.ExecContext(ctx, `UPDATE templates SET
		name = (CASE WHEN ?2 != '' THEN ?2 ELSE name END),
		subject = (CASE WHEN ?3 != '' THEN ?3 ELSE name END),
		body = (CASE WHEN ?4 != '' THEN ?4 ELSE body END),
		body_source = (CASE WHEN ?5 != '' THEN ?5 ELSE body_source END),
		updated_at = ?6
		WHERE id = ?1`, id, t.Name, t.Subject, t.Body, t.BodySource, now())
	if err != nil {
		return models.Template{}, wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return models.Template{}, store.ErrNotFound
	}
	return s.Get(ctx, id, false)
}

func (s templates) SetDefault(ctx context.Context, id int) error {
	// Clear the others first: the unique index allows one default.
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE templates SET is_default = false WHERE id != ?`, id); err != nil {
			return wrap(err)
		}
		_, err := tx.ExecContext(ctx, `UPDATE templates SET is_default = true WHERE id = ? AND type = 'campaign'`, id)
		return wrap(err)
	})
}

func (s templates) Delete(ctx context.Context, id int) error {
	var deleted bool
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var ok bool
		if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM templates WHERE id = ?1 AND is_default = false)
			AND (SELECT COUNT(*) FROM templates) > 1`, id); err != nil {
			return wrap(err)
		}
		if !ok {
			return nil
		}
		// Move campaigns to the default template, then delete.
		if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET template_id = (SELECT id FROM templates
			WHERE is_default = true AND (type = 'campaign' OR type = 'campaign_visual') LIMIT 1) WHERE template_id = ?1`, id); err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM templates WHERE id = ?`, id); err != nil {
			return wrap(err)
		}
		deleted = true
		return nil
	})
	if err != nil {
		return err
	}
	if !deleted {
		return store.ErrNotFound
	}
	return nil
}

// Media.

type mediaRow struct {
	media.Media
	Tags jsonStrings `db:"tags"`
}

func (r mediaRow) model() media.Media {
	m := r.Media
	m.Tags = []string(r.Tags)
	return m
}

func (s mediaStore) Query(ctx context.Context, provider, search string, tags []string, p store.Page) ([]media.Media, int, error) {
	rows := []mediaRow{}
	if search != "" {
		search = strings.ToLower(search)
	}
	pattern := fmt.Sprintf("%%%s%%", search)
	if err := s.r.SelectContext(ctx, &rows, `SELECT COUNT(*) OVER () AS total, * FROM media
		WHERE (?1 = '' OR filename LIKE ?1 OR EXISTS (SELECT 1 FROM json_each(media.tags) t WHERE t.value LIKE ?1))
		AND provider = ?2
		AND (json_array_length(?5) = 0 OR NOT EXISTS (
			SELECT 1 FROM json_each(?5) w WHERE w.value NOT IN (SELECT value FROM json_each(media.tags))))
		ORDER BY created_at DESC, id DESC LIMIT ?4 OFFSET ?3`, pattern, provider, p.Offset, p.Limit, strs(tags)); err != nil {
		return []media.Media{}, 0, wrap(err)
	}
	out := make([]media.Media, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s mediaStore) Get(ctx context.Context, ref store.MediaRef) (media.Media, error) {
	var row mediaRow
	if err := s.r.GetContext(ctx, &row, `SELECT * FROM media WHERE CASE
		WHEN ?1 > 0 THEN id = ?1 WHEN ?2 != '' THEN uuid = ?2 WHEN ?3 != '' THEN filename = ?3 ELSE 0 END`,
		ref.ID, ref.UUID, ref.Filename); err != nil {
		return media.Media{}, notFound(err)
	}
	return row.model(), nil
}

func (s mediaStore) Tags(ctx context.Context, provider string) ([]media.Tag, error) {
	out := []media.Tag{}
	if err := s.r.SelectContext(ctx, &out, `SELECT t.value AS tag, COUNT(DISTINCT media.id) AS count
		FROM media, json_each(media.tags) t WHERE provider = ? GROUP BY t.value ORDER BY t.value`, provider); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s mediaStore) Insert(ctx context.Context, m store.MediaInput) (media.Media, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return media.Media{}, err
	}
	var id int
	if err := s.w.GetContext(ctx, &id, `INSERT INTO media (uuid, filename, thumb, content_type, provider, meta, tags, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, uu.String(), m.Filename, m.Thumb, m.ContentType, m.Provider,
		jsonText(attribsOr(m.Meta)), strs(m.Tags), now()); err != nil {
		return media.Media{}, wrap(err)
	}
	return s.Get(ctx, store.MediaRef{ID: id})
}

func (s mediaStore) SetTags(ctx context.Context, id int, tags []string) (media.Media, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE media SET tags = ? WHERE id = ?`, strs(tags), id)
	if err != nil {
		return media.Media{}, wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return media.Media{}, store.ErrNotFound
	}
	return s.Get(ctx, store.MediaRef{ID: id})
}

func (s mediaStore) Delete(ctx context.Context, id int) (string, error) {
	var fname string
	if err := s.w.GetContext(ctx, &fname, `DELETE FROM media WHERE id = ? RETURNING filename`, id); err != nil {
		return "", wrap(err)
	}
	return fname, nil
}

// Tracking.

func (s tracking) CreateLink(ctx context.Context, url string) (string, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	var out string
	if err := s.w.GetContext(ctx, &out, `INSERT INTO links (uuid, url, created_at) VALUES (?, ?, ?)
		ON CONFLICT (url) DO UPDATE SET url = excluded.url RETURNING uuid`, uu.String(), url, now()); err != nil {
		return "", wrap(err)
	}
	return out, nil
}

func (s tracking) LinkURL(ctx context.Context, linkUUID string) (string, error) {
	var url string
	if err := s.r.GetContext(ctx, &url, `SELECT url FROM links WHERE uuid = ?`, linkUUID); err != nil {
		return "", notFound(err)
	}
	return url, nil
}

func (s tracking) RegisterView(ctx context.Context, campUUID, subUUID string) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var campID int
		if err := tx.GetContext(ctx, &campID, `SELECT id FROM campaigns WHERE uuid = ?`, campUUID); err != nil {
			return notFound(err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO campaign_views (campaign_id, subscriber_id, created_at)
			VALUES (?1, (SELECT id FROM subscribers WHERE ?2 != '' AND uuid = ?2), ?3)`, campID, subUUID, now())
		return wrap(err)
	})
}

func (s tracking) RegisterClick(ctx context.Context, linkUUID, campUUID, subUUID string) (string, error) {
	var url string
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var link struct {
			ID  int    `db:"id"`
			URL string `db:"url"`
		}
		if err := tx.GetContext(ctx, &link, `SELECT id, url FROM links WHERE uuid = ?`, linkUUID); err != nil {
			return notFound(err)
		}
		url = link.URL
		_, err := tx.ExecContext(ctx, `INSERT INTO link_clicks (campaign_id, subscriber_id, link_id, created_at)
			VALUES ((SELECT id FROM campaigns WHERE uuid = ?1), (SELECT id FROM subscribers WHERE ?2 != '' AND uuid = ?2), ?3, ?4)`,
			campUUID, subUUID, link.ID, now())
		return wrap(err)
	})
	return url, err
}

func (s tracking) ExportViews(ctx context.Context, since time.Time, p store.Page) ([]models.CampaignViewExport, error) {
	var out []models.CampaignViewExport
	if err := s.r.SelectContext(ctx, &out, `SELECT v.campaign_id, COALESCE(c.uuid, '') AS campaign_uuid, COALESCE(c.name, '') AS campaign_name,
		COALESCE(v.subscriber_id, 0) AS subscriber_id, COALESCE(s.uuid, '') AS subscriber_uuid, COALESCE(s.email, '') AS email,
		COALESCE(s.name, '') AS subscriber_name, v.created_at
		FROM campaign_views v LEFT JOIN campaigns c ON (c.id = v.campaign_id) LEFT JOIN subscribers s ON (s.id = v.subscriber_id)
		WHERE v.created_at >= ? ORDER BY v.id LIMIT ? OFFSET ?`, ts(since), p.Limit, p.Offset); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s tracking) ExportClicks(ctx context.Context, since time.Time, p store.Page) ([]models.CampaignClickExport, error) {
	var out []models.CampaignClickExport
	if err := s.r.SelectContext(ctx, &out, `SELECT k.campaign_id, COALESCE(c.uuid, '') AS campaign_uuid, COALESCE(c.name, '') AS campaign_name,
		COALESCE(k.subscriber_id, 0) AS subscriber_id, COALESCE(s.uuid, '') AS subscriber_uuid, COALESCE(s.email, '') AS email,
		COALESCE(s.name, '') AS subscriber_name, l.url, k.created_at
		FROM link_clicks k LEFT JOIN campaigns c ON (c.id = k.campaign_id) LEFT JOIN links l ON (l.id = k.link_id)
		LEFT JOIN subscribers s ON (s.id = k.subscriber_id)
		WHERE k.created_at >= ? ORDER BY k.id LIMIT ? OFFSET ?`, ts(since), p.Limit, p.Offset); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s tracking) DeleteViewsBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM campaign_views WHERE created_at < ?`, ts(before))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s tracking) DeleteClicksBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM link_clicks WHERE created_at < ?`, ts(before))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

var _ = errors.Is
var _ = sql.ErrNoRows
