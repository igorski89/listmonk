package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type campaigns struct{ *Store }
type tracking struct{ *Store }

// scopeArgs is upstream's (getAll bool, permittedLists []int) pair.
func scopeArgs(sc store.ListScope) (bool, any) {
	return sc.All, pq.Array(sc.IDs)
}

func (s campaigns) loadStats(out models.Campaigns) error {
	for i := range out {
		// Replace null tags.
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
	return wrap(loadCampaignStats(out, s.q.GetCampaignStats))
}

func (s campaigns) Query(ctx context.Context, f store.CampaignFilter, srt store.Sort, p store.Page) (models.Campaigns, int, error) {
	stmt := replaceOrder(s.q.QueryCampaigns, srt)
	all, permitted := scopeArgs(f.Scope)

	// Unsafe to ignore scanning fields not present in models.Campaigns.
	var rows []campaignRow
	if err := s.db.SelectContext(ctx, &rows, stmt, 0, pq.StringArray(strs(f.Statuses)), pq.StringArray(strs(f.Tags)),
		f.Search, f.Type, all, permitted, p.Offset, p.Limit); err != nil {
		return nil, 0, wrap(err)
	}
	out := campaignModels(rows)
	if err := s.loadStats(out); err != nil {
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
	if err := s.q.GetCampaign.SelectContext(ctx, &rows, ref.ID, uuidArg(ref.UUID), ref.ArchiveSlug, string(tpl)); err != nil {
		return models.Campaign{}, wrap(err)
	}
	out := campaignModels(rows)
	if len(out) == 0 {
		return models.Campaign{}, store.ErrNotFound
	}
	if err := s.loadStats(out); err != nil {
		return models.Campaign{}, err
	}
	return out[0], nil
}

func (s campaigns) GetForPreview(ctx context.Context, id, tplID int) (models.Campaign, error) {
	var row campaignRow
	if err := s.q.GetCampaignForPreview.GetContext(ctx, &row, id, tplID); err != nil {
		return models.Campaign{}, notFound(err)
	}
	return row.model(), nil
}

func (s campaigns) Archived(ctx context.Context, listUUID string, p store.Page) (models.Campaigns, int, error) {
	var rows []campaignRow
	if err := s.q.GetArchivedCampaigns.SelectContext(ctx, &rows, p.Offset, p.Limit, string(store.TemplateArchive), listUUID); err != nil {
		return models.Campaigns{}, 0, wrap(err)
	}
	out := campaignModels(rows)
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s campaigns) Create(ctx context.Context, o models.Campaign, listIDs, mediaIDs []int) (models.Campaign, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return models.Campaign{}, err
	}

	var newID int
	if err := s.q.CreateCampaign.GetContext(ctx, &newID,
		uu,
		o.Type,
		o.Name,
		o.Subject,
		o.FromEmail,
		o.Body,
		o.AltBody,
		o.ContentType,
		o.SendAt,
		o.Headers,
		o.Attribs,
		pq.StringArray(o.Tags),
		o.Messenger,
		o.TemplateID,
		pq.Array(listIDs),
		o.Archive,
		o.ArchiveSlug,
		o.ArchiveTemplateID,
		o.ArchiveMeta,
		pq.Array(mediaIDs),
		o.BodySource,
	); err != nil {
		return models.Campaign{}, wrap(err)
	}

	return s.Get(ctx, store.CampaignRef{ID: newID}, store.TemplateCampaign)
}

func (s campaigns) Update(ctx context.Context, id int, o models.Campaign, listIDs, mediaIDs []int) (models.Campaign, error) {
	if _, err := s.q.UpdateCampaign.ExecContext(ctx, id,
		o.Name,
		o.Subject,
		o.FromEmail,
		o.Body,
		o.AltBody,
		o.ContentType,
		o.SendAt,
		o.Headers,
		o.Attribs,
		pq.StringArray(o.Tags),
		o.Messenger,
		o.TemplateID,
		pq.Array(listIDs),
		o.Archive,
		o.ArchiveSlug,
		o.ArchiveTemplateID,
		o.ArchiveMeta,
		pq.Array(mediaIDs),
		o.BodySource); err != nil {
		return models.Campaign{}, wrap(err)
	}
	return s.Get(ctx, store.CampaignRef{ID: id}, store.TemplateCampaign)
}

func (s campaigns) SetStatus(ctx context.Context, id int, status string) error {
	res, err := s.q.UpdateCampaignStatus.ExecContext(ctx, id, status)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s campaigns) SetArchive(ctx context.Context, id int, a store.ArchiveSettings) error {
	_, err := s.q.UpdateCampaignArchive.ExecContext(ctx, id, a.Enabled, a.Slug, a.TemplateID, a.Meta)
	return wrap(err)
}

func (s campaigns) Delete(ctx context.Context, id int) error {
	res, err := s.q.DeleteCampaign.ExecContext(ctx, id)
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
	res, err := s.q.DeleteCampaigns.ExecContext(ctx, pq.Array(f.IDs), search, f.Scope.All, pq.Array(f.Scope.IDs),
		pq.Array(f.Statuses), pq.Array(f.Tags), f.Type)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s campaigns) HasLists(ctx context.Context, id int, listIDs []int) (bool, error) {
	has := false
	if err := s.q.CampaignHasLists.GetContext(ctx, &has, id, pq.Array(listIDs)); err != nil {
		return false, wrap(err)
	}
	return has, nil
}

func (s campaigns) RunningStats(ctx context.Context, scope store.ListScope) ([]models.CampaignStats, error) {
	out := []models.CampaignStats{}
	if err := s.q.GetCampaignStatus.SelectContext(ctx, &out, models.CampaignStatusRunning, scope.All, pq.Array(scope.IDs)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap(err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s campaigns) Next(ctx context.Context, inProgress []store.CampaignProgress) ([]*models.Campaign, error) {
	var (
		ids   = make([]int64, 0, len(inProgress))
		sent  = make([]int64, 0, len(inProgress))
		lasts = make([]int64, 0, len(inProgress))
	)
	for _, p := range inProgress {
		ids = append(ids, int64(p.ID))
		sent = append(sent, int64(p.Sent))
		lasts = append(lasts, int64(p.LastSubID))
	}

	var rows []campaignRow
	if err := s.q.NextCampaigns.SelectContext(ctx, &rows, pq.Int64Array(ids), pq.Int64Array(sent), pq.Int64Array(lasts)); err != nil {
		return nil, wrap(err)
	}
	var out []*models.Campaign
	for _, r := range rows {
		c := r.model()
		out = append(out, &c)
	}
	return out, nil
}

type runningCamp struct {
	CampaignID      int    `db:"campaign_id"`
	CampaignType    string `db:"campaign_type"`
	MaxSubscriberID int    `db:"max_subscriber_id"`
	ListID          int    `db:"list_id"`
}

func (s campaigns) NextSubscribers(ctx context.Context, campID, afterID, limit int) ([]models.Subscriber, error) {
	var camps []runningCamp
	if err := s.q.GetRunningCampaign.SelectContext(ctx, &camps, campID); err != nil {
		return nil, wrap(err)
	}

	var listIDs []int
	for _, c := range camps {
		listIDs = append(listIDs, c.ListID)
	}
	if len(listIDs) == 0 {
		return nil, nil
	}

	var out []models.Subscriber
	err := s.q.NextCampaignSubscribers.SelectContext(ctx, &out, camps[0].CampaignID, camps[0].CampaignType, afterID, camps[0].MaxSubscriberID, pq.Array(listIDs), limit)
	return out, wrap(err)
}

func (s campaigns) SetCounts(ctx context.Context, id, toSend, sent, lastSubID int) error {
	_, err := s.q.UpdateCampaignCounts.ExecContext(ctx, id, toSend, sent, lastSubID)
	return wrap(err)
}

// Tracking.

func (s tracking) CreateLink(ctx context.Context, url string) (string, error) {
	// Create a new UUID for the URL. If the URL already exists in the DB
	// the UUID in the database is returned.
	uu, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	var out string
	if err := s.q.CreateLink.GetContext(ctx, &out, uu, url); err != nil {
		return "", wrap(err)
	}
	return out, nil
}

func (s tracking) LinkURL(ctx context.Context, linkUUID string) (string, error) {
	var url string
	if err := s.q.GetLinkURL.GetContext(ctx, &url, linkUUID); err != nil {
		return "", notFound(err)
	}
	return url, nil
}

func (s tracking) RegisterView(ctx context.Context, campUUID, subUUID string) error {
	if _, err := s.q.RegisterCampaignView.ExecContext(ctx, campUUID, subUUID); err != nil {
		if e := pqErr(err); e != nil && e.Column == "campaign_id" {
			return store.ErrNotFound
		}
		return wrap(err)
	}
	return nil
}

func (s tracking) RegisterClick(ctx context.Context, linkUUID, campUUID, subUUID string) (string, error) {
	var url string
	if err := s.q.RegisterLinkClick.GetContext(ctx, &url, linkUUID, campUUID, subUUID); err != nil {
		if e := pqErr(err); e != nil && e.Column == "link_id" {
			return "", store.ErrNotFound
		}
		return "", wrap(err)
	}
	return url, nil
}

func (s tracking) ExportViews(ctx context.Context, since time.Time, p store.Page) ([]models.CampaignViewExport, error) {
	var out []models.CampaignViewExport
	if err := s.q.ExportCampaignViews.SelectContext(ctx, &out, since, p.Limit, p.Offset); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s tracking) ExportClicks(ctx context.Context, since time.Time, p store.Page) ([]models.CampaignClickExport, error) {
	var out []models.CampaignClickExport
	if err := s.q.ExportCampaignLinkClicks.SelectContext(ctx, &out, since, p.Limit, p.Offset); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s tracking) DeleteViewsBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.q.DeleteCampaignViews.ExecContext(ctx, before)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s tracking) DeleteClicksBefore(ctx context.Context, before time.Time) (int, error) {
	res, err := s.q.DeleteCampaignLinkClicks.ExecContext(ctx, before)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
