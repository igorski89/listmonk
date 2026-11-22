package postgres

import (
	"context"
	"strings"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type bounces struct{ *Store }

func (s bounces) Record(ctx context.Context, b models.Bounce, action store.BounceAction) error {
	_, err := s.q.RecordBounce.ExecContext(ctx, b.SubscriberUUID,
		b.Email,
		b.CampaignUUID,
		b.Type,
		b.Source,
		b.Meta,
		b.CreatedAt,
		action.Count,
		action.Action)
	if err != nil {
		// The subscriber doesn't exist.
		if e := pqErr(err); e != nil && e.Column == "subscriber_id" {
			return store.ErrNotFound
		}
		return wrap(err)
	}
	return nil
}

func (s bounces) Query(ctx context.Context, f store.BounceFilter, srt store.Sort, p store.Page) ([]models.Bounce, int, error) {
	field := srt.Field
	if field == "campaign_name" {
		field = "campaigns.name"
	}
	dir := models.OrderAsc
	if srt.Desc {
		dir = models.OrderDesc
	}

	out := []models.Bounce{}
	stmt := strings.ReplaceAll(s.q.QueryBounces, "%order%", field+" "+dir+", bounces.id "+dir)
	if err := s.db.SelectContext(ctx, &out, stmt, 0, f.CampaignID, f.SubscriberID, f.Source, f.Type, p.Offset, p.Limit,
		f.Scope.All, pq.Array(f.Scope.IDs)); err != nil {
		return nil, 0, wrap(err)
	}

	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s bounces) Get(ctx context.Context, id int, scope store.ListScope) (models.Bounce, error) {
	var out []models.Bounce
	stmt := strings.ReplaceAll(s.q.QueryBounces, "%order%", "id "+models.OrderAsc)
	if err := s.db.SelectContext(ctx, &out, stmt, id, 0, 0, "", "", 0, 1, scope.All, pq.Array(scope.IDs)); err != nil {
		return models.Bounce{}, wrap(err)
	}
	if len(out) == 0 {
		return models.Bounce{}, store.ErrNotFound
	}
	return out[0], nil
}

func (s bounces) BlocklistSubscribers(ctx context.Context, f store.BounceFilter) error {
	_, err := s.q.BlocklistBouncedSubscribers.ExecContext(ctx, pq.Array(f.IDs), f.CampaignID, f.Source, f.Type,
		f.Scope.All, pq.Array(f.Scope.IDs))
	return wrap(err)
}

func (s bounces) Delete(ctx context.Context, f store.BounceFilter) (int, error) {
	res, err := s.q.DeleteBounces.ExecContext(ctx, pq.Array(f.IDs), f.All, f.CampaignID, f.Source, f.Type,
		f.Scope.All, pq.Array(f.Scope.IDs))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s bounces) DeleteForSubscriber(ctx context.Context, ref store.SubscriberRef) error {
	_, err := s.q.DeleteBouncesBySubscriber.ExecContext(ctx, ref.ID, uuidArg(ref.UUID))
	return wrap(err)
}
