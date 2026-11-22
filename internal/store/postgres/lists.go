package postgres

import (
	"context"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type lists struct{ *Store }

// replaceOrder fills upstream's %order% placeholder.
func replaceOrder(query string, srt store.Sort) string {
	return strings.ReplaceAll(query, "%order%", sortExpr(srt))
}

func fixListTags(out []models.List) {
	for i := range out {
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
}

func (s lists) Get(ctx context.Context, ref store.ListRef, scope store.ListScope) (models.List, error) {
	var rows []listRow
	stmt := replaceOrder(s.q.QueryLists, store.Sort{Field: models.FieldCreatedAt, Desc: true})
	var permitted any
	if !scope.All {
		permitted = pq.Array(scope.IDs)
	}
	if err := s.db.SelectContext(ctx, &rows, stmt, ref.ID, uuidArg(ref.UUID), "", "", "", "", pq.StringArray{}, scope.All, permitted, 0, 1); err != nil {
		return models.List{}, wrap(err)
	}
	res := listModels(rows)
	if len(res) == 0 {
		return models.List{}, store.ErrNotFound
	}
	fixListTags(res)
	return res[0], nil
}

func (s lists) All(ctx context.Context, typ, status string, scope store.ListScope) ([]models.List, error) {
	rows := []listRow{}
	if err := s.q.GetLists.SelectContext(ctx, &rows, typ, status, "id", scope.All, pq.Array(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	out := listModels(rows)
	for i, l := range out {
		if l.Tags == nil {
			out[i].Tags = []string{}
		}
		// Total counts.
		for _, c := range l.SubscriberCounts {
			out[i].SubscriberCount += c
		}
	}
	return out, nil
}

func (s lists) Query(ctx context.Context, f store.ListFilter, srt store.Sort, p store.Page) ([]models.List, int, error) {
	_ = s.refreshCache(ctx, store.StatsListSubscribers)

	rows := []listRow{}
	stmt := replaceOrder(s.q.QueryLists, srt)
	if err := s.db.SelectContext(ctx, &rows, stmt, 0, "", f.Search, f.Type, f.Optin, f.Status, pq.StringArray(strs(f.Tags)),
		f.Scope.All, pq.Array(f.Scope.IDs), p.Offset, p.Limit); err != nil {
		return nil, 0, wrap(err)
	}
	out := listModels(rows)

	total := 0
	if len(out) > 0 {
		total = out[0].Total
		fixListTags(out)
	}
	return out, total, nil
}

func (s lists) ByOptin(ctx context.Context, ids []int, optin string) ([]models.List, error) {
	rows := []listRow{}
	if err := s.q.GetListsByOptin.SelectContext(ctx, &rows, optin, pq.Array(ids), nil); err != nil {
		return nil, wrap(err)
	}
	return listModels(rows), nil
}

func (s lists) Types(ctx context.Context, ids []int, uuids []string) (map[any]string, error) {
	res := []struct {
		ID   int    `db:"id"`
		UUID string `db:"uuid"`
		Type string `db:"type"`
	}{}
	if err := s.q.GetListTypes.SelectContext(ctx, &res, pq.Array(ids), pq.StringArray(uuids)); err != nil {
		return nil, wrap(err)
	}

	out := map[any]string{}
	isIDs := ids != nil
	for _, r := range res {
		if isIDs {
			out[r.ID] = r.Type
		} else {
			out[r.UUID] = r.Type
		}
	}
	return out, nil
}

func (s lists) Create(ctx context.Context, l models.List) (models.List, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return models.List{}, err
	}

	var newID int
	if err := s.q.CreateList.GetContext(ctx, &newID, uu.String(), l.Name, l.Type, l.Optin, l.Status, pq.StringArray(l.Tags), l.Description); err != nil {
		return models.List{}, wrap(err)
	}
	return s.Get(ctx, store.ListRef{ID: newID}, store.ListScope{All: true})
}

func (s lists) Update(ctx context.Context, id int, l models.List) (models.List, error) {
	res, err := s.q.UpdateList.ExecContext(ctx, id, l.Name, l.Type, l.Optin, l.Status, pq.StringArray(l.Tags), l.Description)
	if err != nil {
		return models.List{}, wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return models.List{}, store.ErrNotFound
	}
	return s.Get(ctx, store.ListRef{ID: id}, store.ListScope{All: true})
}

func (s lists) DeleteMany(ctx context.Context, f store.ListFilter) (int, error) {
	search := f.Search
	if len(f.IDs) > 0 {
		search = ""
	}
	res, err := s.q.DeleteLists.ExecContext(ctx, pq.Array(f.IDs), search, f.Type, f.Optin, f.Status, pq.StringArray(strs(f.Tags)),
		f.Scope.All, pq.Array(f.Scope.IDs))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s lists) Touch(ctx context.Context, ids []int) error {
	_, err := s.q.UpdateListsDate.ExecContext(ctx, pq.Array(ids))
	return wrap(err)
}
