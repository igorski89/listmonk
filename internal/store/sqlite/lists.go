package sqlite

import (
	"context"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

type lists struct{ *Store }

type listRow struct {
	models.List
	Tags jsonStrings `db:"tags"`
}

func (r listRow) model() models.List {
	l := r.List
	l.Tags = []string(r.Tags)
	if l.Tags == nil {
		l.Tags = []string{}
	}
	return l
}

func listModels(rows []listRow) []models.List {
	if rows == nil {
		return nil
	}
	out := make([]models.List, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out
}

// Enum columns sort in their declaration order, as in Postgres.
const (
	listStatusOrder         = `CASE lists.status WHEN 'active' THEN 0 WHEN 'archived' THEN 1 END`
	subscriberStatusOrder   = `CASE subscribers.status WHEN 'enabled' THEN 0 WHEN 'disabled' THEN 1 WHEN 'blocklisted' THEN 2 END`
	subscriptionStatusOrder = `CASE subscriber_lists.status WHEN 'unconfirmed' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'unsubscribed' THEN 2 END`
	campaignStatusOrder     = `CASE campaigns.status WHEN 'draft' THEN 0 WHEN 'running' THEN 1 WHEN 'scheduled' THEN 2 WHEN 'paused' THEN 3 WHEN 'cancelled' THEN 4 WHEN 'finished' THEN 5 END`
)

var listSortFields = map[string]string{
	"name":             "lists.name",
	"status":           listStatusOrder,
	"created_at":       "lists.created_at",
	"updated_at":       "lists.updated_at",
	"subscriber_count": "subscriber_count",
}

// queryListsSQL is query-lists: lists with live subscriber counts per
// subscription status (Postgres reads them from a materialized view).
const queryListsSQL = `
WITH statuses AS (
	SELECT list_id, json_group_object(status, cnt) AS subscriber_statuses, SUM(cnt) AS subscriber_count
	FROM (SELECT list_id, status, COUNT(*) AS cnt FROM subscriber_lists GROUP BY list_id, status)
	GROUP BY list_id
)
SELECT COUNT(*) OVER () AS total, lists.*,
	COALESCE(statuses.subscriber_statuses, '{}') AS subscriber_statuses,
	COALESCE(statuses.subscriber_count, 0) AS subscriber_count
FROM lists LEFT JOIN statuses ON (lists.id = statuses.list_id)
WHERE (CASE
		WHEN ?1 > 0 THEN lists.id = ?1
		WHEN ?2 != '' THEN lists.uuid = ?2
		WHEN ?3 != '' THEN lists.name LIKE '%' || ?3 || '%'
		ELSE 1
	END)
	AND (?4 = '' OR lists.type = ?4)
	AND (?5 = '' OR lists.optin = ?5)
	AND (?6 = '' OR lists.status = ?6)
	AND (json_array_length(?7) = 0 OR NOT EXISTS (
		SELECT 1 FROM json_each(?7) t WHERE t.value NOT IN (SELECT value FROM json_each(lists.tags))))
	AND (?8 OR lists.id IN (SELECT value FROM json_each(?9)))
ORDER BY {ORDER} LIMIT ?11 OFFSET ?10`

func limit(n int) int {
	if n < 1 {
		return -1
	}
	return n
}

func (s lists) Get(ctx context.Context, ref store.ListRef, scope store.ListScope) (models.List, error) {
	var rows []listRow
	q := withOrder(queryListsSQL, "lists.created_at DESC")
	if err := s.r.SelectContext(ctx, &rows, q, ref.ID, ref.UUID, "", "", "", "", "[]", scope.All, ints(scope.IDs), 0, 1); err != nil {
		return models.List{}, wrap(err)
	}
	if len(rows) == 0 {
		return models.List{}, store.ErrNotFound
	}
	return rows[0].model(), nil
}

func (s lists) All(ctx context.Context, typ, status string, scope store.ListScope) ([]models.List, error) {
	rows := []listRow{}
	if err := s.r.SelectContext(ctx, &rows, `SELECT * FROM lists
		WHERE (?1 = '' OR type = ?1) AND (?2 = '' OR status = ?2)
		AND (?3 OR id IN (SELECT value FROM json_each(?4)))
		ORDER BY id`, typ, status, scope.All, ints(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	out := listModels(rows)
	for i, l := range out {
		for _, c := range l.SubscriberCounts {
			out[i].SubscriberCount += c
		}
	}
	return out, nil
}

func (s lists) Query(ctx context.Context, f store.ListFilter, srt store.Sort, p store.Page) ([]models.List, int, error) {
	rows := []listRow{}
	q := withOrder(queryListsSQL, orderBy(srt, listSortFields, "lists.created_at")+", lists.id")
	if err := s.r.SelectContext(ctx, &rows, q, 0, "", f.Search, f.Type, f.Optin, f.Status, strs(f.Tags),
		f.Scope.All, ints(f.Scope.IDs), p.Offset, limit(p.Limit)); err != nil {
		return nil, 0, wrap(err)
	}
	out := listModels(rows)
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s lists) ByOptin(ctx context.Context, ids []int, optin string) ([]models.List, error) {
	rows := []listRow{}
	if ids == nil {
		return []models.List{}, nil
	}
	if err := s.r.SelectContext(ctx, &rows, `SELECT * FROM lists WHERE (?1 = '' OR optin = ?1)
		AND id IN (SELECT value FROM json_each(?2)) ORDER BY name`, optin, ints(ids)); err != nil {
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
	out := map[any]string{}
	var err error
	switch {
	case ids != nil:
		err = s.r.SelectContext(ctx, &res, `SELECT id, uuid, type FROM lists WHERE id IN (SELECT value FROM json_each(?1))`, ints(ids))
	case uuids != nil:
		err = s.r.SelectContext(ctx, &res, `SELECT id, uuid, type FROM lists WHERE uuid IN (SELECT value FROM json_each(?1))`, strs(uuids))
	default:
		return out, nil
	}
	if err != nil {
		return nil, wrap(err)
	}
	for _, r := range res {
		if ids != nil {
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
	t := now()
	res, err := s.w.ExecContext(ctx, `INSERT INTO lists (uuid, name, type, optin, status, tags, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, uu.String(), l.Name, l.Type, l.Optin, l.Status, strs(l.Tags), l.Description, t, t)
	if err != nil {
		return models.List{}, wrap(err)
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, store.ListRef{ID: int(id)}, store.ListScope{All: true})
}

func (s lists) Update(ctx context.Context, id int, l models.List) (models.List, error) {
	var n int64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE lists SET
			name = (CASE WHEN ?2 != '' THEN ?2 ELSE name END),
			type = (CASE WHEN ?3 != '' THEN ?3 ELSE type END),
			optin = (CASE WHEN ?4 != '' THEN ?4 ELSE optin END),
			status = (CASE WHEN ?5 != '' THEN ?5 ELSE status END),
			tags = ?6,
			description = (CASE WHEN ?7 != '' THEN ?7 ELSE description END),
			updated_at = ?8
			WHERE id = ?1`, id, l.Name, l.Type, l.Optin, l.Status, strs(l.Tags), l.Description, now())
		if err != nil {
			return wrap(err)
		}
		if n, _ = res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE campaign_lists SET list_name = (SELECT name FROM lists WHERE id = ?1) WHERE list_id = ?1`, id)
		return wrap(err)
	})
	if err != nil {
		return models.List{}, err
	}
	if n == 0 {
		return models.List{}, store.ErrNotFound
	}
	return s.Get(ctx, store.ListRef{ID: id}, store.ListScope{All: true})
}

func (s lists) DeleteMany(ctx context.Context, f store.ListFilter) (int, error) {
	search := f.Search
	if len(f.IDs) > 0 {
		search = ""
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM lists WHERE
		(CASE WHEN json_array_length(?1) > 0 THEN id IN (SELECT value FROM json_each(?1))
			ELSE (?2 = '' OR name LIKE '%' || ?2 || '%') END)
		AND (json_array_length(?1) > 0 OR (
			(?3 = '' OR type = ?3)
			AND (?4 = '' OR optin = ?4)
			AND (?5 = '' OR status = ?5)
			AND (json_array_length(?6) = 0 OR NOT EXISTS (
				SELECT 1 FROM json_each(?6) t WHERE t.value NOT IN (SELECT value FROM json_each(lists.tags))))
		))
		AND (?7 OR id IN (SELECT value FROM json_each(?8)))`,
		ints(f.IDs), search, f.Type, f.Optin, f.Status, strs(f.Tags), f.Scope.All, ints(f.Scope.IDs))
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s lists) Touch(ctx context.Context, ids []int) error {
	_, err := s.w.ExecContext(ctx, `UPDATE lists SET updated_at = ?2 WHERE id IN (SELECT value FROM json_each(?1))`, ints(ids), now())
	return wrap(err)
}

