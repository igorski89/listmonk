package storetest

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

func testSchema(t *testing.T, st store.Store) {
	ok, err := st.Schema().Installed(ctx)
	must(t, err)
	eq(t, ok, true, "installed")

	pending, err := st.Schema().Pending(ctx)
	must(t, err)
	eq(t, len(pending), 0, "pending migrations after install")

	info, err := st.Schema().Info(ctx)
	must(t, err)
	eq(t, info.Dialect, st.Dialect(), "info dialect")
	if info.Version == "" {
		t.Errorf("info version empty")
	}
	must(t, st.Schema().Optimize(ctx))

	// Nothing to migrate right after install.
	must(t, st.Schema().Migrate(ctx, store.MigrationEnv{Log: log.New(io.Discard, "", 0)}, true))
	v, err := st.Schema().Version(ctx)
	must(t, err)
	if v == "" || v == "v0.0.0" {
		t.Errorf("version after install: %q", v)
	}
}

func testSettings(t *testing.T, st store.Store) {
	raw, err := st.Settings().All(ctx)
	must(t, err)
	var s map[string]any
	must(t, json.Unmarshal(raw, &s))
	if _, ok := s["app.root_url"]; !ok {
		t.Fatalf("default settings missing app.root_url: %v", s)
	}

	must(t, st.Settings().Set(ctx, "app.site_name", json.RawMessage(`"Parity"`)))
	raw, err = st.Settings().All(ctx)
	must(t, err)
	must(t, json.Unmarshal(raw, &s))
	eq(t, s["app.site_name"], any("Parity"), "set one key")

	// Update writes all given keys.
	s["app.site_name"] = "Both"
	s["app.root_url"] = "http://example.com"
	b, _ := json.Marshal(s)
	must(t, st.Settings().Update(ctx, b))
	raw, err = st.Settings().All(ctx)
	must(t, err)
	must(t, json.Unmarshal(raw, &s))
	eq(t, s["app.site_name"], any("Both"), "update site name")
	eq(t, s["app.root_url"], any("http://example.com"), "update root url")
}

func testLists(t *testing.T, st store.Store) {
	f := seed(t, st)
	L := st.Lists()

	// Get by ID and by UUID.
	l, err := L.Get(ctx, store.ListRef{ID: f.pubDouble.ID}, all)
	must(t, err)
	eq(t, l.Name, "Weekly news", "get by id")
	eq(t, l.Tags, []string{"news", "weekly"}, "tags")
	eq(t, l.Optin, "double", "optin")
	l, err = L.Get(ctx, store.ListRef{UUID: f.privSingle.UUID}, all)
	must(t, err)
	eq(t, l.ID, f.privSingle.ID, "get by uuid")
	_, err = L.Get(ctx, store.ListRef{ID: 9999}, all)
	isErr(t, err, store.ErrNotFound, "get missing")
	eq(t, f.pubSingle.Tags, []string{}, "empty tags are [] not nil")

	// Update: empty fields keep their value, tags are replaced.
	l, err = L.Update(ctx, f.pubDouble.ID, models.List{Name: "Weekly", Tags: []string{"x"}})
	must(t, err)
	eq(t, l.Name, "Weekly", "updated name")
	eq(t, l.Type, "public", "type kept")
	eq(t, l.Optin, "double", "optin kept")
	eq(t, l.Description, "weekly", "description kept")
	eq(t, l.Tags, []string{"x"}, "tags replaced")
	_, err = L.Update(ctx, 9999, models.List{Name: "x"})
	isErr(t, err, store.ErrNotFound, "update missing")

	// Query: filters, sort, paging, total.
	out, total, err := L.Query(ctx, store.ListFilter{Scope: all}, store.Sort{Field: "name"}, store.Page{Limit: 2})
	must(t, err)
	eq(t, total, 3, "total")
	eq(t, listNames(out), []string{"Announcements", "Customers"}, "sort by name, limit 2")
	out, _, err = L.Query(ctx, store.ListFilter{Scope: all}, store.Sort{Field: "name"}, store.Page{Offset: 2, Limit: 2})
	must(t, err)
	eq(t, listNames(out), []string{"Weekly"}, "offset")
	out, _, err = L.Query(ctx, store.ListFilter{Type: "public", Scope: all}, store.Sort{Field: "name", Desc: true}, store.Page{})
	must(t, err)
	eq(t, listNames(out), []string{"Weekly", "Announcements"}, "type filter, desc")
	out, _, err = L.Query(ctx, store.ListFilter{Search: "custom", Scope: all}, store.Sort{Field: "name"}, store.Page{})
	must(t, err)
	eq(t, listNames(out), []string{"Customers"}, "search")
	out, _, err = L.Query(ctx, store.ListFilter{Tags: []string{"crm"}, Scope: all}, store.Sort{Field: "name"}, store.Page{})
	must(t, err)
	eq(t, listNames(out), []string{"Customers"}, "tag filter")
	out, _, err = L.Query(ctx, store.ListFilter{Optin: "double", Scope: all}, store.Sort{Field: "name"}, store.Page{})
	must(t, err)
	eq(t, listNames(out), []string{"Weekly"}, "optin filter")
	out, total, err = L.Query(ctx, store.ListFilter{Scope: scope(f.privSingle.ID)}, store.Sort{Field: "name"}, store.Page{})
	must(t, err)
	eq(t, listNames(out), []string{"Customers"}, "scoped")
	eq(t, total, 1, "scoped total")
	// Query refreshes the subscriber counts (Get reads the cached ones).
	out, _, err = L.Query(ctx, store.ListFilter{Scope: all}, store.Sort{Field: "subscriber_count", Desc: true}, store.Page{Limit: 1})
	must(t, err)
	eq(t, listNames(out), []string{"Customers"}, "sort by subscriber count")
	eq(t, out[0].SubscriberCount, 3, "subscriber count")
	eq(t, out[0].SubscriberCounts, models.StringIntMap{"confirmed": 2, "unconfirmed": 1}, "subscriber counts by status")

	// All, ByOptin, Types.
	out, err = L.All(ctx, "", "", all)
	must(t, err)
	eq(t, len(out), 3, "all")
	eq(t, out[0].ID < out[1].ID && out[1].ID < out[2].ID, true, "all ordered by id")
	out, err = L.All(ctx, "public", "", scope(f.pubSingle.ID))
	must(t, err)
	eq(t, listNames(out), []string{"Announcements"}, "all: type + scope")
	out, err = L.ByOptin(ctx, []int{f.pubDouble.ID, f.privSingle.ID}, "double")
	must(t, err)
	eq(t, listNames(out), []string{"Weekly"}, "by optin")
	types, err := L.Types(ctx, []int{f.pubDouble.ID, f.privSingle.ID}, nil)
	must(t, err)
	eq(t, types, map[any]string{f.pubDouble.ID: "public", f.privSingle.ID: "private"}, "types by id")
	types, err = L.Types(ctx, nil, []string{f.pubDouble.UUID})
	must(t, err)
	eq(t, types, map[any]string{f.pubDouble.UUID: "public"}, "types by uuid")

	must(t, L.Touch(ctx, []int{f.pubSingle.ID}))

	// DeleteMany: scope applies; filter; IDs.
	n, err := L.DeleteMany(ctx, store.ListFilter{IDs: []int{f.pubSingle.ID}, Scope: scope(f.privSingle.ID)})
	must(t, err)
	eq(t, n, 0, "delete outside scope")
	n, err = L.DeleteMany(ctx, store.ListFilter{Search: "announce", Scope: all})
	must(t, err)
	eq(t, n, 1, "delete by search")
	n, err = L.DeleteMany(ctx, store.ListFilter{IDs: []int{f.pubDouble.ID}, Scope: all})
	must(t, err)
	eq(t, n, 1, "delete by id")
	eq(t, subStatuses(t, st, f.anna.ID), map[int]string{f.privSingle.ID: "unconfirmed"}, "subscriptions of a deleted list go")
}

func testSubscribers(t *testing.T, st store.Store, fx Fixtures) {
	f := seed(t, st)
	S := st.Subscribers()

	// Insert returns the subscriber with its lists.
	eq(t, f.anna.Name, "Anna", "insert name")
	eq(t, f.anna.Status, "enabled", "insert status")
	if f.anna.UUID == "" || f.anna.ID == 0 {
		t.Fatalf("insert: no id/uuid: %+v", f.anna)
	}
	eq(t, subStatuses(t, st, f.anna.ID), map[int]string{f.pubDouble.ID: "unconfirmed", f.privSingle.ID: "unconfirmed"}, "insert subscriptions (single opt-in not auto-confirmed)")
	eq(t, subStatuses(t, st, f.ben.ID), map[int]string{f.pubDouble.ID: "confirmed", f.privSingle.ID: "confirmed"}, "preconfirmed")

	// By UUID, and blocklisted subscribers' subscriptions are unsubscribed.
	eve, err := S.Insert(ctx, store.SubscriberInput{Email: "eve@example.com", Name: "Eve", Status: "blocklisted", Attribs: models.JSON{}},
		store.ListChange{ListUUIDs: []string{f.pubSingle.UUID}, Status: "confirmed"})
	must(t, err)
	eq(t, subStatuses(t, st, eve.ID), map[int]string{f.pubSingle.ID: "unsubscribed"}, "blocklisted insert")

	_, err = S.Insert(ctx, store.SubscriberInput{Email: "anna@example.com", Name: "Dup", Status: "enabled"}, store.ListChange{Status: "unconfirmed"})
	conflict(t, err, "email", "duplicate e-mail")

	// Get: by ID, UUID, e-mail; ID wins.
	s, err := S.Get(ctx, store.SubscriberRef{UUID: f.ben.UUID})
	must(t, err)
	eq(t, s.ID, f.ben.ID, "get by uuid")
	s, err = S.Get(ctx, store.SubscriberRef{Email: "cara@example.com"})
	must(t, err)
	eq(t, s.ID, f.cara.ID, "get by email")
	s, err = S.Get(ctx, store.SubscriberRef{ID: f.ben.ID, Email: "cara@example.com"})
	must(t, err)
	eq(t, s.ID, f.ben.ID, "id wins over email")
	_, err = S.Get(ctx, store.SubscriberRef{ID: 9999})
	isErr(t, err, store.ErrNotFound, "get missing")
	eq(t, s.Attribs["city"], any("Paris"), "attribs")

	subs, err := S.GetByEmails(ctx, []string{"anna@example.com", "nobody@example.com", "cara@example.com"})
	must(t, err)
	eq(t, sorted(subEmails(subs)), []string{"anna@example.com", "cara@example.com"}, "get by emails")
	if len(subs[0].ParsedLists()) == 0 {
		t.Errorf("get by emails: lists not loaded")
	}

	// Update: empty fields keep their value; attribs replaced.
	s, err = S.Update(ctx, f.cara.ID, store.SubscriberInput{Name: "Cara C", Attribs: models.JSON{"vip": true}})
	must(t, err)
	eq(t, s.Name, "Cara C", "update name")
	eq(t, s.Email, "cara@example.com", "email kept")
	eq(t, s.Status, "enabled", "status kept")
	eq(t, s.Attribs, models.JSON{"vip": true}, "attribs replaced")
	_, err = S.Update(ctx, f.cara.ID, store.SubscriberInput{Email: "ben@example.com"})
	conflict(t, err, "email", "update to a duplicate e-mail")

	// UpdateWithLists: add, keep, replace within permitted lists.
	_, err = S.UpdateWithLists(ctx, f.cara.ID, store.SubscriberInput{}, store.ListChange{ListIDs: []int{f.pubSingle.ID}, Status: "confirmed"})
	must(t, err)
	eq(t, subStatuses(t, st, f.cara.ID), map[int]string{f.privSingle.ID: "confirmed", f.pubSingle.ID: "confirmed"}, "update with lists: add")
	_, err = S.UpdateWithLists(ctx, f.cara.ID, store.SubscriberInput{}, store.ListChange{ListIDs: []int{f.pubDouble.ID}, Status: "unconfirmed",
		ReplaceExisting: true, Permitted: scope(f.pubSingle.ID, f.pubDouble.ID)})
	must(t, err)
	eq(t, subStatuses(t, st, f.cara.ID), map[int]string{f.privSingle.ID: "confirmed", f.pubDouble.ID: "unconfirmed"}, "replace within permitted lists")

	// Replacing with all lists permitted deletes every other subscription.
	fay, err := S.Insert(ctx, store.SubscriberInput{Email: "fay@example.com", Name: "Fay", Status: "enabled", Attribs: models.JSON{}},
		store.ListChange{ListIDs: []int{f.pubSingle.ID, f.pubDouble.ID}, Status: "confirmed"})
	must(t, err)
	_, err = S.UpdateWithLists(ctx, fay.ID, store.SubscriberInput{}, store.ListChange{ListIDs: []int{f.privSingle.ID}, Status: "confirmed",
		ReplaceExisting: true, Permitted: all})
	must(t, err)
	eq(t, subStatuses(t, st, fay.ID), map[int]string{f.privSingle.ID: "confirmed"}, "replace, all lists permitted")

	// Re-subscribing: an unsubscribed subscription stays unsubscribed unless
	// AllowResubscribe; confirmed ones stay confirmed.
	must(t, st.Subscriptions().Unsubscribe(ctx, []int{f.cara.ID}, []int{f.pubDouble.ID}, nil))
	_, err = S.UpdateWithLists(ctx, f.cara.ID, store.SubscriberInput{}, store.ListChange{ListIDs: []int{f.pubDouble.ID, f.privSingle.ID}, Status: "unconfirmed"})
	must(t, err)
	eq(t, subStatuses(t, st, f.cara.ID)[f.pubDouble.ID], "unsubscribed", "no resubscribe")
	eq(t, subStatuses(t, st, f.cara.ID)[f.privSingle.ID], "confirmed", "confirmed stays")
	_, err = S.UpdateWithLists(ctx, f.cara.ID, store.SubscriberInput{}, store.ListChange{ListIDs: []int{f.pubDouble.ID, f.privSingle.ID}, Status: "unconfirmed", AllowResubscribe: true})
	must(t, err)
	eq(t, subStatuses(t, st, f.cara.ID)[f.pubDouble.ID], "unconfirmed", "resubscribe")
	eq(t, subStatuses(t, st, f.cara.ID)[f.privSingle.ID], "confirmed", "confirmed stays on resubscribe")

	// HasLists and Lists.
	has, err := S.HasLists(ctx, []int{f.anna.ID, f.dan.ID}, []int{f.privSingle.ID})
	must(t, err)
	eq(t, has[f.anna.ID], true, "has lists")
	eq(t, has[f.dan.ID], false, "has no lists")
	ls, err := S.Lists(ctx, store.SubscriberRef{ID: f.anna.ID}, nil, nil, "unconfirmed", "double")
	must(t, err)
	eq(t, listNames(ls), []string{"Weekly news"}, "lists: status + optin")
	ls, err = S.Lists(ctx, store.SubscriberRef{UUID: f.anna.UUID}, nil, []string{f.privSingle.UUID}, "", "")
	must(t, err)
	eq(t, listNames(ls), []string{"Customers"}, "lists by uuid")

	// Profile, activity, charts: JSON shapes.
	p, err := S.Profile(ctx, store.SubscriberRef{ID: f.anna.ID}, all)
	must(t, err)
	// The profile is a JSON array with one object.
	var prof []map[string]any
	must(t, json.Unmarshal(p.Profile, &prof))
	eq(t, len(prof), 1, "profile: one object")
	for _, k := range []string{"email", "name", "attribs", "uuid"} {
		if _, ok := prof[0][k]; !ok {
			t.Errorf("profile: missing %q in %s", k, p.Profile)
		}
	}
	var pl []map[string]any
	must(t, json.Unmarshal(p.Subscriptions, &pl))
	eq(t, len(pl), 2, "profile: subscriptions")
	_, err = S.Activity(ctx, f.anna.ID, all)
	must(t, err)
	ch, err := S.Charts(ctx, f.anna.ID, all)
	must(t, err)
	if !json.Valid(ch) {
		t.Errorf("charts: not JSON: %s", ch)
	}
	must(t, S.DeleteActivity(ctx, f.anna.ID, store.ActivityCampaignViews))
	must(t, S.DeleteActivity(ctx, f.anna.ID, store.ActivityLinkClicks))

	// Blocklist: unsubscribes from all lists.
	must(t, S.Blocklist(ctx, []int{f.ben.ID}))
	s, _ = S.Get(ctx, store.SubscriberRef{ID: f.ben.ID})
	eq(t, s.Status, "blocklisted", "blocklisted")
	eq(t, subStatuses(t, st, f.ben.ID), map[int]string{f.pubDouble.ID: "unsubscribed", f.privSingle.ID: "unsubscribed"}, "blocklist unsubscribes")

	// UnsubscribeByCampaign: three modes; unknown UUIDs are a no-op.
	camp, err := st.Campaigns().Create(ctx, models.Campaign{Type: "regular", Name: "c", Subject: "s", FromEmail: "a@b.c", Body: "b",
		ContentType: "richtext", Messenger: "email", Headers: models.Headers{}, Attribs: models.JSON{}, ArchiveMeta: json.RawMessage("{}")},
		[]int{f.privSingle.ID}, nil)
	must(t, err)
	must(t, S.UnsubscribeByCampaign(ctx, "00000000-0000-0000-0000-000000000000", camp.UUID, false, false))
	must(t, S.UnsubscribeByCampaign(ctx, f.anna.UUID, camp.UUID, false, false))
	eq(t, subStatuses(t, st, f.anna.ID), map[int]string{f.pubDouble.ID: "unconfirmed", f.privSingle.ID: "unsubscribed"}, "unsubscribe from the campaign's lists")
	must(t, S.UnsubscribeByCampaign(ctx, f.anna.UUID, camp.UUID, false, true))
	eq(t, subStatuses(t, st, f.anna.ID), map[int]string{f.pubDouble.ID: "unsubscribed", f.privSingle.ID: "unsubscribed"}, "unsubscribe all unconfirmed")
	must(t, S.UnsubscribeByCampaign(ctx, f.cara.UUID, camp.UUID, true, false))
	s, _ = S.Get(ctx, store.SubscriberRef{ID: f.cara.ID})
	eq(t, s.Status, "blocklisted", "unsubscribe + blocklist")
	for _, st := range subStatuses(t, st, f.cara.ID) {
		eq(t, st, "unsubscribed", "blocklist unsubscribes all")
	}

	// Maintenance deletes.
	n, err := S.DeleteOrphans(ctx)
	must(t, err)
	eq(t, n, 1, "delete orphans (dan)")
	n, err = S.DeleteBlocklisted(ctx)
	must(t, err)
	eq(t, n, 3, "delete blocklisted (ben, cara, eve)")
	must(t, S.Delete(ctx, nil, []string{f.anna.UUID}))
	must(t, S.Delete(ctx, []int{fay.ID}, nil))
	_, err = S.Get(ctx, store.SubscriberRef{ID: f.anna.ID})
	isErr(t, err, store.ErrNotFound, "deleted by uuid")
	_ = fx
}

func testSubscriberFilters(t *testing.T, st store.Store, fx Fixtures) {
	f := seed(t, st)
	S := st.Subscribers()
	q := func(fl store.SubscriberFilter) ([]string, int) {
		t.Helper()
		out, total, err := S.Query(ctx, fl, store.Sort{Field: "email"}, store.Page{Limit: 100})
		must(t, err)
		return subEmails(out), total
	}

	got, total := q(store.SubscriberFilter{})
	eq(t, got, []string{"anna@example.com", "ben@example.com", "cara@example.com", "dan@example.com"}, "all, sorted by email")
	eq(t, total, 4, "total")
	got, _ = q(store.SubscriberFilter{Search: "ben"})
	eq(t, got, []string{"ben@example.com"}, "search")
	got, _ = q(store.SubscriberFilter{ListIDs: []int{f.pubDouble.ID}})
	eq(t, got, []string{"anna@example.com", "ben@example.com"}, "list filter")
	got, _ = q(store.SubscriberFilter{ListIDs: []int{f.pubDouble.ID}, SubscriptionStatus: "confirmed"})
	eq(t, got, []string{"ben@example.com"}, "list + subscription status")
	got, _ = q(store.SubscriberFilter{SubscriptionStatus: "none"})
	eq(t, got, []string{"dan@example.com"}, "orphans")
	got, _ = q(store.SubscriberFilter{Expr: fx.CityBerlin})
	eq(t, got, []string{"anna@example.com", "dan@example.com"}, "expression")
	got, _ = q(store.SubscriberFilter{Expr: fx.CityBerlin, ListIDs: []int{f.privSingle.ID}})
	eq(t, got, []string{"anna@example.com"}, "expression + list")

	// Lists are loaded on query results.
	out, _, err := S.Query(ctx, store.SubscriberFilter{Search: "anna"}, store.Sort{}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out[0].ParsedLists()), 2, "lists loaded")

	// Sort desc and paging; total is independent of the page.
	out, total, err = S.Query(ctx, store.SubscriberFilter{}, store.Sort{Field: "name", Desc: true}, store.Page{Offset: 1, Limit: 2})
	must(t, err)
	eq(t, subEmails(out), []string{"cara@example.com", "ben@example.com"}, "sort desc + page")
	eq(t, total, 4, "total with paging")
	out, total, err = S.Query(ctx, store.SubscriberFilter{Search: "nobody"}, store.Sort{}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 0, "no results")
	eq(t, total, 0, "no results total")

	// Rejected expressions.
	_, _, err = S.Query(ctx, store.SubscriberFilter{Expr: fx.ForbiddenTable}, store.Sort{}, store.Page{Limit: 10})
	invalidQuery(t, err, "forbidden table")
	_, _, err = S.Query(ctx, store.SubscriberFilter{Expr: fx.Write}, store.Sort{}, store.Page{Limit: 10})
	invalidQuery(t, err, "write attempt")
	_, err = S.DeleteByFilter(ctx, store.SubscriberFilter{Expr: fx.ForbiddenTable})
	invalidQuery(t, err, "forbidden table in a write by filter")
	_, err = S.Export(ctx, store.SubscriberFilter{Expr: fx.Write})
	invalidQuery(t, err, "write attempt in export")
	got, _ = q(store.SubscriberFilter{})
	eq(t, len(got), 4, "nothing deleted by rejected expressions")

	// Export: batches by id.
	next, err := S.Export(ctx, store.SubscriberFilter{ListIDs: []int{f.privSingle.ID}})
	must(t, err)
	b1, err := next(0, 2)
	must(t, err)
	eq(t, len(b1), 2, "export batch 1")
	b2, err := next(b1[len(b1)-1].ID, 2)
	must(t, err)
	eq(t, len(b2), 1, "export batch 2")
	b3, err := next(b2[0].ID, 2)
	must(t, err)
	eq(t, len(b3), 0, "export done")
	next, err = S.Export(ctx, store.SubscriberFilter{IDs: []int{f.dan.ID, f.cara.ID}})
	must(t, err)
	b1, err = next(0, 10)
	must(t, err)
	eq(t, len(b1), 2, "export selected ids")

	// Writes by filter return the number of subscribers matched.
	n, err := S.BlocklistByFilter(ctx, store.SubscriberFilter{Expr: fx.CityBerlin})
	must(t, err)
	eq(t, n, 2, "blocklist by filter")
	s, _ := S.Get(ctx, store.SubscriberRef{ID: f.dan.ID})
	eq(t, s.Status, "blocklisted", "blocklisted by filter")
	got, _ = q(store.SubscriberFilter{Status: "blocklisted"})
	eq(t, got, []string{"anna@example.com", "dan@example.com"}, "subscriber status filter")
	n, err = S.DeleteByFilter(ctx, store.SubscriberFilter{Search: "dan"})
	must(t, err)
	eq(t, n, 1, "delete by filter")
	n, err = S.DeleteByFilter(ctx, store.SubscriberFilter{Search: "nobody"})
	must(t, err)
	eq(t, n, 0, "delete by filter, no match")
	got, _ = q(store.SubscriberFilter{})
	eq(t, got, []string{"anna@example.com", "ben@example.com", "cara@example.com"}, "after deletes")
}

func testSubscriptions(t *testing.T, st store.Store, fx Fixtures) {
	f := seed(t, st)
	SS := st.Subscriptions()

	subs, err := SS.List(ctx, store.SubscriberRef{ID: f.anna.ID}, false)
	must(t, err)
	eq(t, len(subs), 2, "list subscriptions")
	eq(t, subs[0].Tags != nil, true, "subscription list tags not nil")
	subs, err = SS.List(ctx, store.SubscriberRef{ID: f.dan.ID}, true)
	must(t, err)
	eq(t, len(subs), 3, "all lists")
	eq(t, subs[0].SubscriptionStatus.Valid, false, "all lists: no subscription")

	// Add: with a status, existing ones change too; with "", new ones
	// are unconfirmed and existing ones keep theirs.
	must(t, SS.Add(ctx, []int{f.anna.ID, f.dan.ID}, []int{f.pubSingle.ID}, ""))
	eq(t, subStatuses(t, st, f.dan.ID), map[int]string{f.pubSingle.ID: "unconfirmed"}, "add, no status")
	must(t, SS.Add(ctx, []int{f.anna.ID}, []int{f.pubDouble.ID}, ""))
	eq(t, subStatuses(t, st, f.anna.ID)[f.pubDouble.ID], "unconfirmed", "add, no status, existing kept")
	must(t, SS.Add(ctx, []int{f.anna.ID}, []int{f.pubDouble.ID}, "confirmed"))
	eq(t, subStatuses(t, st, f.anna.ID)[f.pubDouble.ID], "confirmed", "add with status overwrites")

	must(t, SS.Unsubscribe(ctx, []int{f.anna.ID}, nil, []string{f.pubSingle.UUID}))
	eq(t, subStatuses(t, st, f.anna.ID)[f.pubSingle.ID], "unsubscribed", "unsubscribe by uuid")
	subs, err = SS.List(ctx, store.SubscriberRef{UUID: f.anna.UUID}, false)
	must(t, err)
	eq(t, len(subs), 3, "list includes unsubscribed")

	must(t, SS.Remove(ctx, []int{f.anna.ID}, []int{f.pubSingle.ID}))
	_, ok := subStatuses(t, st, f.anna.ID)[f.pubSingle.ID]
	eq(t, ok, false, "removed")

	// By filter: f.ListIDs are the source lists, listIDs the targets.
	n, err := SS.AddByFilter(ctx, store.SubscriberFilter{ListIDs: []int{f.privSingle.ID}}, []int{f.pubSingle.ID}, "confirmed")
	must(t, err)
	eq(t, n, 3, "add by filter")
	eq(t, subStatuses(t, st, f.cara.ID)[f.pubSingle.ID], "confirmed", "added by filter")
	n, err = SS.UnsubscribeByFilter(ctx, store.SubscriberFilter{Expr: fx.CityBerlin}, []int{f.pubSingle.ID})
	must(t, err)
	eq(t, n, 2, "unsubscribe by filter")
	eq(t, subStatuses(t, st, f.anna.ID)[f.pubSingle.ID], "unsubscribed", "unsubscribed by filter")
	n, err = SS.RemoveByFilter(ctx, store.SubscriberFilter{Search: "cara"}, []int{f.pubSingle.ID})
	must(t, err)
	eq(t, n, 1, "remove by filter")
	_, ok = subStatuses(t, st, f.cara.ID)[f.pubSingle.ID]
	eq(t, ok, false, "removed by filter")
	_, err = SS.AddByFilter(ctx, store.SubscriberFilter{Expr: fx.ForbiddenTable}, []int{f.pubSingle.ID}, "")
	invalidQuery(t, err, "add by forbidden filter")

	// Opt-in confirmation. (The meta it merges isn't readable through
	// the store: get-subscriptions aliases it to a column the model
	// doesn't map.)
	must(t, SS.ConfirmOptin(ctx, f.anna.UUID, []string{f.privSingle.UUID}, models.JSON{"confirm_ip": "1.2.3.4"}))
	eq(t, subStatuses(t, st, f.anna.ID)[f.privSingle.ID], "confirmed", "confirm optin")

	// Unconfirmed subscriptions to double opt-in lists created before a time.
	must(t, SS.Add(ctx, []int{f.dan.ID}, []int{f.pubDouble.ID}, "unconfirmed"))
	n, err = SS.DeleteUnconfirmed(ctx, time.Now().Add(-time.Hour))
	must(t, err)
	eq(t, n, 0, "delete unconfirmed, none old enough")
	n, err = SS.DeleteUnconfirmed(ctx, time.Now().Add(time.Hour))
	must(t, err)
	eq(t, n, 1, "delete unconfirmed (dan's double opt-in; his single opt-in one stays)")
	eq(t, subStatuses(t, st, f.dan.ID), map[int]string{f.pubSingle.ID: "unsubscribed"}, "other subscriptions kept")
}

func testImport(t *testing.T, st store.Store) {
	f := seed(t, st)
	S := st.Subscribers()
	opt := store.ImportOptions{Mode: store.ImportSubscribe, ListIDs: []int{f.pubSingle.ID}, SubscriptionStatus: "confirmed", Permitted: all}

	// New subscribers, and an existing one without overwrite.
	must(t, S.Import(ctx, []store.SubscriberInput{
		{Email: "new@example.com", Name: "New", Attribs: models.JSON{"a": 1}},
		{Email: "anna@example.com", Name: "Anna Imported", Attribs: models.JSON{"city": "Rome"}},
	}, opt))
	s, err := S.Get(ctx, store.SubscriberRef{Email: "new@example.com"})
	must(t, err)
	eq(t, s.Status, "enabled", "imported status")
	eq(t, subStatuses(t, st, s.ID), map[int]string{f.pubSingle.ID: "confirmed"}, "imported subscription")
	s, _ = S.Get(ctx, store.SubscriberRef{ID: f.anna.ID})
	eq(t, s.Name, "Anna", "no overwrite: name kept")
	eq(t, subStatuses(t, st, f.anna.ID)[f.pubSingle.ID], "confirmed", "existing subscriber subscribed")

	// Overwrite user info and subscription status.
	opt.OverwriteUserInfo, opt.OverwriteSubStatus, opt.SubscriptionStatus, opt.ListIDs = true, true, "unconfirmed", []int{f.privSingle.ID}
	must(t, S.Import(ctx, []store.SubscriberInput{{Email: "ben@example.com", Name: "Ben Imported", Attribs: models.JSON{"city": "Rome"}}}, opt))
	s, _ = S.Get(ctx, store.SubscriberRef{ID: f.ben.ID})
	eq(t, s.Name, "Ben Imported", "overwrite name")
	eq(t, s.Attribs, models.JSON{"city": "Rome"}, "overwrite attribs")
	eq(t, subStatuses(t, st, f.ben.ID)[f.privSingle.ID], "unconfirmed", "overwrite subscription status")

	// Blocklist mode.
	must(t, S.Import(ctx, []store.SubscriberInput{{Email: "cara@example.com", Name: "Cara"}, {Email: "zed@example.com", Name: "Zed"}},
		store.ImportOptions{Mode: store.ImportBlocklist, Permitted: all}))
	for _, e := range []string{"cara@example.com", "zed@example.com"} {
		s, err = S.Get(ctx, store.SubscriberRef{Email: e})
		must(t, err)
		eq(t, s.Status, "blocklisted", "blocklist import "+e)
	}
	for _, st := range subStatuses(t, st, f.cara.ID) {
		eq(t, st, "unsubscribed", "blocklist import unsubscribes")
	}

	// A batch is atomic: one bad row, nothing written.
	err = S.Import(ctx, []store.SubscriberInput{{Email: "ok@example.com", Name: "OK"}, {Email: "bad@example.com", Name: "Bad", Status: "nonsense"}},
		store.ImportOptions{Mode: store.ImportSubscribe, ListIDs: []int{9999}, SubscriptionStatus: "confirmed", Permitted: all})
	if err == nil {
		t.Errorf("import into a missing list: want an error")
	}
	_, err = S.Get(ctx, store.SubscriberRef{Email: "ok@example.com"})
	isErr(t, err, store.ErrNotFound, "failed batch rolled back")
}
