package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type subscribers struct{ *Store }
type subscriptions struct{ *Store }

var allowedSubQueryTables = map[string]struct{}{
	"subscribers":      {},
	"lists":            {},
	"subscriber_lists": {},
	"campaigns":        {},
	"campaign_lists":   {},
	"campaign_views":   {},
	"links":            {},
	"link_clicks":      {},
	"bounces":          {},
}

func uuidArg(u string) any {
	if u == "" {
		return nil
	}
	return u
}

func ints(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s subscribers) loadLists(out models.Subscribers) error {
	return wrap(loadSubscriberLists(out, s.q.GetSubscriberListsLazy))
}

func (s subscribers) Get(ctx context.Context, ref store.SubscriberRef) (models.Subscriber, error) {
	var out models.Subscribers
	if err := s.q.GetSubscriber.SelectContext(ctx, &out, ref.ID, uuidArg(ref.UUID), ref.Email); err != nil {
		return models.Subscriber{}, wrap(err)
	}
	if len(out) == 0 {
		return models.Subscriber{}, store.ErrNotFound
	}
	if err := s.loadLists(out); err != nil {
		return models.Subscriber{}, err
	}
	return out[0], nil
}

func (s subscribers) GetByEmails(ctx context.Context, emails []string) (models.Subscribers, error) {
	var out models.Subscribers
	if err := s.q.GetSubscribersByEmails.SelectContext(ctx, &out, pq.Array(emails)); err != nil {
		return nil, wrap(err)
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := s.loadLists(out); err != nil {
		return nil, err
	}
	return out, nil
}

// subSort maps a logical sort field to upstream's ORDER BY column.
func subSort(srt store.Sort) string {
	if srt.Field == "" {
		srt.Field = "subscribers.id"
	}
	return sortExpr(srt)
}

func (s subscribers) Query(ctx context.Context, f store.SubscriberFilter, srt store.Sort, p store.Page) (models.Subscribers, int, error) {
	listIDs := ints(f.ListIDs)

	// There's an arbitrary query condition.
	cond := "TRUE"
	if f.Expr != "" {
		cond = f.Expr
	}

	// Handle the special "none" subscription status for orphan subscribers.
	cond, subStatus := models.ApplyOrphanFilter(cond, f.SubscriptionStatus)

	stmt := strings.ReplaceAll(s.q.QuerySubscribers, "%query%", cond)
	stmt = strings.ReplaceAll(stmt, "%order%", subSort(srt))

	// Validate the tables used in the query.
	if err := validateQueryTables(ctx, s.db, stmt, allowedSubQueryTables, pq.Array(listIDs), subStatus, f.Search, p.Offset, p.Limit, f.Status); err != nil {
		return nil, 0, &store.InvalidQueryError{Msg: err.Error(), Err: err}
	}

	// A readonly transaction that just does COUNT() to obtain the count of results
	// and to ensure that the arbitrary query is indeed readonly.
	total, err := s.count(ctx, f.Search, cond, subStatus, f.Status, listIDs)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return models.Subscribers{}, 0, nil
	}

	tx, err := s.txReadOnly(ctx)
	if err != nil {
		return nil, 0, &store.InvalidQueryError{Msg: dbErrMsg(err), Err: err}
	}
	defer tx.Rollback()

	var out models.Subscribers
	if err := tx.SelectContext(ctx, &out, stmt, pq.Array(listIDs), subStatus, f.Search, p.Offset, p.Limit, f.Status); err != nil {
		return nil, 0, wrap(err)
	}
	if err := s.loadLists(out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// count returns the number of subscribers matching a condition.
func (s subscribers) count(ctx context.Context, search, queryExp, subStatus, subscriberStatus string, listIDs []int) (int, error) {
	// If there's no condition, it's a "get all" call which can probably be optionally pulled from cache.
	// The cached stats don't track the subscriber status, so skip the cache when there's a query filter.
	if queryExp == "" && subscriberStatus == "" {
		_ = s.refreshCache(ctx, store.StatsListSubscribers)

		total := 0
		if err := s.q.QuerySubscribersCountAll.GetContext(ctx, &total, pq.Array(listIDs), subStatus); err != nil {
			return 0, wrap(err)
		}
		return total, nil
	}

	stmt := strings.ReplaceAll(s.q.QuerySubscribersCount, "%query%", queryExp)
	tx, err := s.txReadOnly(ctx)
	if err != nil {
		return 0, &store.InvalidQueryError{Msg: dbErrMsg(err), Err: err}
	}
	defer tx.Rollback()

	total := 0
	if err := tx.GetContext(ctx, &total, stmt, pq.Array(listIDs), subStatus, search, subscriberStatus); err != nil {
		return 0, wrap(err)
	}
	return total, nil
}

func (s subscribers) Export(ctx context.Context, f store.SubscriberFilter) (func(afterID, limit int) ([]models.SubscriberExport, error), error) {
	var (
		subIDs  = ints(f.IDs)
		listIDs = ints(f.ListIDs)
	)

	cond := "TRUE"
	if f.Expr != "" {
		cond = f.Expr
	}
	cond, subStatus := models.ApplyOrphanFilter(cond, f.SubscriptionStatus)

	stmt := strings.ReplaceAll(s.q.QuerySubscribersForExport, "%query%", cond)

	if err := validateQueryTables(ctx, s.db, stmt, allowedSubQueryTables,
		pq.Array(listIDs), 0, pq.Array(subIDs), subStatus, f.Search, 0, f.Status); err != nil {
		return nil, &store.InvalidQueryError{Msg: err.Error(), Err: err}
	}

	if _, err := s.count(ctx, f.Search, cond, subStatus, f.Status, listIDs); err != nil {
		return nil, err
	}

	pstmt, err := s.db.PreparexContext(ctx, stmt)
	if err != nil {
		return nil, &store.InvalidQueryError{Msg: dbErrMsg(err), Err: err}
	}

	return func(afterID, limit int) ([]models.SubscriberExport, error) {
		var out []models.SubscriberExport
		if err := pstmt.Select(&out, pq.Array(listIDs), afterID, pq.Array(subIDs), subStatus, f.Search, limit, f.Status); err != nil {
			return nil, wrap(err)
		}
		return out, nil
	}, nil
}

func (s subscribers) HasLists(ctx context.Context, subIDs, listIDs []int) (map[int]bool, error) {
	res := []struct {
		SubID int  `db:"subscriber_id"`
		Has   bool `db:"has"`
	}{}
	if err := s.q.HasSubscriberLists.SelectContext(ctx, &res, pq.Array(subIDs), pq.Array(listIDs)); err != nil {
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
	if err := s.q.GetSubscriberLists.SelectContext(ctx, &rows, ref.ID, uuidArg(ref.UUID), pq.Array(ints(listIDs)), pq.Array(strs(listUUIDs)), subStatus, listType); err != nil {
		return nil, wrap(err)
	}
	return listModels(rows), nil
}

func (s subscribers) Profile(ctx context.Context, ref store.SubscriberRef, scope store.ListScope) (models.SubscriberExportProfile, error) {
	var out models.SubscriberExportProfile
	if err := s.q.ExportSubscriberData.GetContext(ctx, &out, ref.ID, uuidArg(ref.UUID), scope.All, pq.Array(scope.IDs)); err != nil {
		return models.SubscriberExportProfile{}, wrap(err)
	}
	return out, nil
}

func (s subscribers) Activity(ctx context.Context, id int, scope store.ListScope) (models.SubscriberActivity, error) {
	var out models.SubscriberActivity
	if err := s.q.GetSubscriberActivity.GetContext(ctx, &out, id, scope.All, pq.Array(scope.IDs)); err != nil {
		return models.SubscriberActivity{}, wrap(err)
	}
	return out, nil
}

func (s subscribers) Charts(ctx context.Context, id int, scope store.ListScope) (json.RawMessage, error) {
	var out types.JSONText
	if err := s.q.GetSubscriberCharts.GetContext(ctx, &out, id, scope.All, pq.Array(scope.IDs)); err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(out), nil
}

func (s subscribers) DeleteActivity(ctx context.Context, id int, kind store.ActivityKind) error {
	var stmt *sqlx.Stmt
	switch kind {
	case store.ActivityCampaignViews:
		stmt = s.q.DeleteSubscriberCampaignViews
	case store.ActivityLinkClicks:
		stmt = s.q.DeleteSubscriberLinkClicks
	default:
		return fmt.Errorf("unknown activity kind %q", kind)
	}
	_, err := stmt.ExecContext(ctx, id)
	return wrap(err)
}

func (s subscribers) Insert(ctx context.Context, in store.SubscriberInput, lists store.ListChange) (models.Subscriber, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return models.Subscriber{}, err
	}

	var id int
	if err := s.q.InsertSubscriber.GetContext(ctx, &id,
		uu.String(),
		in.Email,
		in.Name,
		in.Status,
		in.Attribs,
		pq.Array(ints(lists.ListIDs)),
		pq.Array(strs(lists.ListUUIDs)),
		lists.Status); err != nil {
		if e := pqErr(err); e != nil && e.Constraint == "subscribers_email_key" {
			return models.Subscriber{}, &store.ConflictError{Field: "email", Err: err}
		}
		return models.Subscriber{}, wrap(err)
	}

	return s.Get(ctx, store.SubscriberRef{ID: id, Email: in.Email})
}

func attribsJSON(a models.JSON) (json.RawMessage, error) {
	if len(a) == 0 {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (s subscribers) Update(ctx context.Context, id int, in store.SubscriberInput) (models.Subscriber, error) {
	attribs, err := attribsJSON(in.Attribs)
	if err != nil {
		return models.Subscriber{}, err
	}
	if _, err := s.q.UpdateSubscriber.ExecContext(ctx, id, in.Email, in.Name, in.Status, attribs); err != nil {
		return models.Subscriber{}, wrap(err)
	}
	return s.Get(ctx, store.SubscriberRef{ID: id})
}

func (s subscribers) UpdateWithLists(ctx context.Context, id int, in store.SubscriberInput, lists store.ListChange) (models.Subscriber, error) {
	attribs, err := attribsJSON(in.Attribs)
	if err != nil {
		return models.Subscriber{}, err
	}
	var permitted []int
	if !lists.Permitted.All {
		permitted = lists.Permitted.IDs
	}
	if _, err := s.q.UpdateSubscriberWithLists.ExecContext(ctx, id,
		in.Email,
		in.Name,
		in.Status,
		attribs,
		pq.Array(lists.ListIDs),
		pq.Array(lists.ListUUIDs),
		lists.Status,
		lists.ReplaceExisting,
		pq.Array(permitted),
		lists.AllowResubscribe); err != nil {
		return models.Subscriber{}, wrap(err)
	}
	return s.Get(ctx, store.SubscriberRef{ID: id})
}

func (s subscribers) Import(ctx context.Context, rows []store.SubscriberInput, o store.ImportOptions) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()

	var stmt *sqlx.Stmt
	if o.Mode == store.ImportBlocklist {
		stmt = tx.StmtxContext(ctx, s.q.UpsertBlocklistSubscriber)
	} else {
		stmt = tx.StmtxContext(ctx, s.q.UpsertSubscriber)
	}

	for _, r := range rows {
		uu, err := uuid.NewV4()
		if err != nil {
			return err
		}
		if o.Mode == store.ImportBlocklist {
			_, err = stmt.ExecContext(ctx, uu, r.Email, r.Name, r.Attribs)
		} else {
			_, err = stmt.ExecContext(ctx, uu, r.Email, r.Name, r.Attribs, pq.Array(o.ListIDs), o.SubscriptionStatus, o.OverwriteUserInfo, o.OverwriteSubStatus)
		}
		if err != nil {
			return wrap(err)
		}
	}
	return wrap(tx.Commit())
}

func (s subscribers) Blocklist(ctx context.Context, ids []int) error {
	_, err := s.q.BlocklistSubscribers.ExecContext(ctx, pq.Array(ids))
	return wrap(err)
}

func (s subscribers) BlocklistByFilter(ctx context.Context, f store.SubscriberFilter) (int, error) {
	return s.execByFilter(ctx, f, s.q.BlocklistSubscribersByQuery)
}

func (s subscribers) Delete(ctx context.Context, ids []int, uuids []string) error {
	_, err := s.q.DeleteSubscribers.ExecContext(ctx, pq.Array(ints(ids)), pq.Array(strs(uuids)))
	return wrap(err)
}

func (s subscribers) DeleteByFilter(ctx context.Context, f store.SubscriberFilter) (int, error) {
	return s.execByFilter(ctx, f, s.q.DeleteSubscribersByQuery)
}

func (s subscribers) DeleteOrphans(ctx context.Context) (int, error) {
	res, err := s.q.DeleteOrphanSubscribers.ExecContext(ctx)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s subscribers) DeleteBlocklisted(ctx context.Context) (int, error) {
	res, err := s.q.DeleteBlocklistedSubscribers.ExecContext(ctx)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s subscribers) UnsubscribeByCampaign(ctx context.Context, subUUID, campUUID string, blocklist, allUnconfirmed bool) error {
	_, err := s.q.UnsubscribeByCampaign.ExecContext(ctx, campUUID, subUUID, blocklist, allUnconfirmed)
	return wrap(err)
}

// execByFilter resolves an arbitrary subscriber filter to IDs in a
// read-only transaction, then runs a writable query template on those IDs
// only. The user expression never reaches the writable statement.
func (s *Store) execByFilter(ctx context.Context, f store.SubscriberFilter, baseQueryTpl string, args ...any) (int, error) {
	queryExp, subStatus := models.ApplyOrphanFilter(f.Expr, f.SubscriptionStatus)

	ids, err := s.querySubscriberIDs(ctx, f.Search, queryExp, ints(f.ListIDs), subStatus, f.Status)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	stmt := strings.ReplaceAll(baseQueryTpl, "%query%", "SELECT UNNEST($1::INT[]) AS id")
	a := append([]any{pq.Array(ids)}, args...)
	if _, err := s.db.ExecContext(ctx, stmt, a...); err != nil {
		return 0, wrap(err)
	}
	return len(ids), nil
}

// querySubscriberIDs resolves an arbitrary WHERE expression to subscriber IDs
// inside a read-only transaction. Only these IDs may be used in a writable query.
func (s *Store) querySubscriberIDs(ctx context.Context, search, queryExp string, listIDs []int, subStatus, subscriberStatus string) ([]int, error) {
	cond := "TRUE"
	if queryExp != "" {
		cond = queryExp
	}

	// Validate the same query and arguments used to select the IDs.
	stmt := strings.ReplaceAll(s.q.QuerySubscribersTpl, "%query%", cond)
	args := []any{false, pq.Array(listIDs), subStatus, search, subscriberStatus}
	if err := validateQueryTables(ctx, s.db, stmt, allowedSubQueryTables, args...); err != nil {
		return nil, &store.InvalidQueryError{Msg: err.Error(), Err: err}
	}

	tx, err := s.txReadOnly(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer tx.Rollback()

	var ids []int
	if err := tx.SelectContext(ctx, &ids, stmt, args...); err != nil {
		return nil, wrap(err)
	}
	return ids, nil
}

// validateQueryTables checks if the query accesses only allowed tables.
func validateQueryTables(ctx context.Context, db *sqlx.DB, query string, allowedTables map[string]struct{}, args ...any) error {
	// Get the EXPLAIN (FORMAT JSON) output.
	tx, err := db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var plan string
	if err = tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan); err != nil {
		return err
	}

	// Extract all relation names from the JSON plan.
	tables, err := getTablesFromQueryPlan(plan)
	if err != nil {
		return fmt.Errorf("error getting tables from query: %v", err)
	}

	// Validate against allowed tables.
	for _, table := range tables {
		if _, ok := allowedTables[table]; !ok {
			return fmt.Errorf("table '%s' is not allowed", table)
		}
	}
	return nil
}

// getTablesFromQueryPlan parses the EXPLAIN JSON to find all "Relation Name" entries.
func getTablesFromQueryPlan(explainJSON string) ([]string, error) {
	var plans []map[string]any
	if err := json.Unmarshal([]byte(explainJSON), &plans); err != nil {
		return nil, err
	}

	// Collect table names in `tables` recursively.
	tables := make(map[string]struct{})
	for _, plan := range plans {
		traverseQueryPlan(plan, tables)
	}

	result := make([]string, 0, len(tables))
	for table := range tables {
		result = append(result, table)
	}
	return result, nil
}

func traverseQueryPlan(node map[string]any, tables map[string]struct{}) {
	if relName, ok := node["Relation Name"].(string); ok {
		tables[relName] = struct{}{}
	}

	// Recursively check nested plans (e.g., subqueries, CTEs).
	for _, v := range node {
		switch v := v.(type) {
		case map[string]any:
			traverseQueryPlan(v, tables)
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					traverseQueryPlan(m, tables)
				}
			}
		}
	}
}

// dbErrMsg is upstream's pqErrMsg: the error with Postgres' DETAIL.
func dbErrMsg(err error) string {
	if e := pqErr(err); e != nil && e.Detail != "" {
		return fmt.Sprintf("%s. %s", e, e.Detail)
	}
	return err.Error()
}

// Subscriptions.

func (s subscriptions) List(ctx context.Context, ref store.SubscriberRef, allLists bool) ([]models.Subscription, error) {
	var rows []subscriptionRow
	if err := s.q.GetSubscriptions.SelectContext(ctx, &rows, ref.ID, ref.UUID, allLists); err != nil {
		return nil, wrap(err)
	}
	if rows == nil {
		return nil, nil
	}
	out := make([]models.Subscription, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out, nil
}

func (s subscriptions) Add(ctx context.Context, subIDs, listIDs []int, status string) error {
	_, err := s.q.AddSubscribersToLists.ExecContext(ctx, pq.Array(subIDs), pq.Array(listIDs), status)
	return wrap(err)
}

func (s subscriptions) AddByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int, status string) (int, error) {
	return s.execByFilter(ctx, f, s.q.AddSubscribersToListsByQuery, pq.Array(listIDs), status)
}

func (s subscriptions) Remove(ctx context.Context, subIDs, listIDs []int) error {
	_, err := s.q.DeleteSubscriptions.ExecContext(ctx, pq.Array(subIDs), pq.Array(listIDs))
	return wrap(err)
}

func (s subscriptions) RemoveByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int) (int, error) {
	return s.execByFilter(ctx, f, s.q.DeleteSubscriptionsByQuery, pq.Array(listIDs))
}

func (s subscriptions) Unsubscribe(ctx context.Context, subIDs, listIDs []int, listUUIDs []string) error {
	_, err := s.q.UnsubscribeSubscribersFromLists.ExecContext(ctx, pq.Array(subIDs), pq.Array(listIDs), pq.StringArray(listUUIDs))
	return wrap(err)
}

func (s subscriptions) UnsubscribeByFilter(ctx context.Context, f store.SubscriberFilter, listIDs []int) (int, error) {
	return s.execByFilter(ctx, f, s.q.UnsubscribeSubscribersFromListsByQuery, pq.Array(listIDs))
}

func (s subscriptions) ConfirmOptin(ctx context.Context, subUUID string, listUUIDs []string, meta models.JSON) error {
	if meta == nil {
		meta = models.JSON{}
	}
	_, err := s.q.ConfirmSubscriptionOptin.ExecContext(ctx, subUUID, pq.Array(listUUIDs), meta)
	return wrap(err)
}

func (s subscriptions) DeleteUnconfirmed(ctx context.Context, before time.Time) (int, error) {
	res, err := s.q.DeleteUnconfirmedSubscriptions.ExecContext(ctx, before)
	if err != nil {
		return 0, wrap(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
