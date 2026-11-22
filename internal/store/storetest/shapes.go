package storetest

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

// shape describes a JSON document's structure: object keys (sorted) with
// their value shapes, the first element of arrays, and scalar kinds. It
// pins down the JSON that queries build, which the frontend reads, without
// pinning values like timestamps.
func shape(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("shape: not JSON: %v: %s", err, raw)
	}
	return shapeOf(v)
}

func shapeOf(v any) string {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+shapeOf(v[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		return "[" + shapeOf(v[0]) + "]"
	case string:
		return "str"
	case float64:
		return "num"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return "?"
}

// testSorting pins orderings where databases naturally differ.
func testSorting(t *testing.T, st store.Store) {
	f := seed(t, st)

	// Enum columns sort in declaration order, not alphabetically:
	// subscriber status enabled < disabled < blocklisted.
	must(t, st.Subscribers().Blocklist(ctx, []int{f.anna.ID}))
	_, err := st.Subscribers().Update(ctx, f.ben.ID, store.SubscriberInput{Status: "disabled"})
	must(t, err)
	out, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Search: "a"}, store.Sort{Field: "status"}, store.Page{Limit: 10})
	must(t, err)
	if len(out) > 0 {
		eq(t, out[0].Status, "enabled", "subscriber status sorts by enum order (enabled first)")
		eq(t, out[len(out)-1].Status, "blocklisted", "subscriber status sorts by enum order (blocklisted last)")
	}

	// Campaign status: draft < running < scheduled < paused < cancelled < finished.
	C := st.Campaigns()
	a, err := C.Create(ctx, newCampaign("a"), nil, nil)
	must(t, err)
	b, err := C.Create(ctx, newCampaign("b"), nil, nil)
	must(t, err)
	must(t, C.SetStatus(ctx, a.ID, "cancelled"))
	cs, _, err := C.Query(ctx, store.CampaignFilter{Scope: all}, store.Sort{Field: "status"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, campNames(cs), []string{"b", "a"}, "campaign status sorts by enum order (draft before cancelled)")

	// NULLs sort last ascending, first descending.
	c := newCampaign("c")
	c.SendAt = null.TimeFrom(time.Now().Add(time.Hour))
	_, err = C.Create(ctx, c, nil, nil)
	must(t, err)
	cs, _, err = C.Query(ctx, store.CampaignFilter{Scope: all}, store.Sort{Field: "send_at"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, cs[0].Name, "c", "non-null send_at first ascending")
	cs, _, err = C.Query(ctx, store.CampaignFilter{Scope: all}, store.Sort{Field: "send_at", Desc: true}, store.Page{Limit: 10})
	must(t, err)
	eq(t, cs[len(cs)-1].Name, "c", "non-null send_at last descending")
	_ = b

	// Mixed case: names sort in byte order, upper case first (Postgres'
	// C / C.UTF-8 collation, the default of the official Alpine image,
	// which SGNL runs; a database created with another locale differs).
	_, err = st.Lists().Create(ctx, models.List{Name: "alpha", Type: "private", Optin: "single", Status: "active", Tags: []string{}})
	must(t, err)
	_, err = st.Lists().Create(ctx, models.List{Name: "Beta", Type: "private", Optin: "single", Status: "active", Tags: []string{}})
	must(t, err)
	ls, _, err := st.Lists().Query(ctx, store.ListFilter{Search: "a", Scope: all}, store.Sort{Field: "name"}, store.Page{Limit: 10})
	must(t, err)
	names := listNames(ls)
	ia, ib := -1, -1
	for i, n := range names {
		if n == "alpha" {
			ia = i
		}
		if n == "Beta" {
			ib = i
		}
	}
	eq(t, ib < ia, true, "Beta sorts before alpha")
}

// testShapes pins the structure of JSON built in SQL.
func testShapes(t *testing.T, st store.Store) {
	f := seed(t, st)
	tpl, err := st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `{{ template "content" . }}`})
	must(t, err)
	must(t, st.Templates().SetDefault(ctx, tpl.ID))
	m, err := st.Media().Insert(ctx, store.MediaInput{Filename: "a.pdf", ContentType: "application/pdf", Provider: "filesystem", Meta: models.JSON{}})
	must(t, err)
	camp, err := st.Campaigns().Create(ctx, newCampaign("c"), []int{f.privSingle.ID}, []int{m.ID})
	must(t, err)
	link, err := st.Tracking().CreateLink(ctx, "https://example.com/x")
	must(t, err)
	must(t, st.Tracking().RegisterView(ctx, camp.UUID, f.anna.UUID))
	_, err = st.Tracking().RegisterClick(ctx, link, camp.UUID, f.anna.UUID)
	must(t, err)
	must(t, st.Bounces().Record(ctx, models.Bounce{Email: "anna@example.com", CampaignUUID: camp.UUID, Type: "soft", Source: "api",
		Meta: json.RawMessage(`{"r":1}`), CreatedAt: time.Now()}, store.BounceAction{Count: 9, Action: "none"}))

	check := func(what string, raw []byte, want string) {
		t.Helper()
		eq(t, shape(t, raw), want, what)
	}

	s, err := st.Subscribers().Get(ctx, store.SubscriberRef{ID: f.anna.ID})
	must(t, err)
	check("subscriber lists", s.Lists, "[{created_at:str,description:str,id:num,name:str,optin:str,status:str,subscription_created_at:str,subscription_meta:{},subscription_status:str,subscription_updated_at:str,tags:[str],type:str,updated_at:str,uuid:str}]")

	c, err := st.Campaigns().Get(ctx, store.CampaignRef{ID: camp.ID}, store.TemplateCampaign)
	must(t, err)
	check("campaign lists", c.Lists, "[{id:num,name:str}]")
	check("campaign media", c.Media, "[{filename:str,id:num}]")

	p, err := st.Subscribers().Profile(ctx, store.SubscriberRef{ID: f.anna.ID}, all)
	must(t, err)
	check("profile", p.Profile, "[{attribs:{city:str,n:num},created_at:str,email:str,id:num,name:str,status:str,updated_at:str,uuid:str}]")
	check("profile subscriptions", p.Subscriptions, "[{created_at:str,name:str,subscription_status:str,type:str}]")
	check("profile views", p.CampaignViews, "[{campaign:str,views:num}]")
	check("profile clicks", p.LinkClicks, "[{clicks:num,url:str}]")

	a, err := st.Subscribers().Activity(ctx, f.anna.ID, all)
	must(t, err)
	check("activity views", a.CampaignViews, "[{id:num,last_viewed_at:str,name:str,subject:str,uuid:str,view_count:num}]")
	check("activity clicks", a.LinkClicks, "[{campaign_id:num,campaign_name:str,campaign_subject:str,campaign_uuid:str,click_count:num,last_clicked_at:str,link_id:num,url:str}]")

	ch, err := st.Subscribers().Charts(ctx, f.anna.ID, all)
	must(t, err)
	check("subscriber charts", ch, "{campaign_views:[{count:num,date:str}],link_clicks:[{count:num,date:str}]}")

	raw, err := st.Stats().DashboardCounts(ctx)
	must(t, err)
	check("dashboard counts", raw, "{campaigns:{by_status:{draft:num},total:num},lists:{optin_double:num,optin_single:num,private:num,public:num,total:num},messages:num,subscribers:{blocklisted:null,orphans:num,total:num}}")
	raw, err = st.Stats().DashboardCharts(ctx)
	must(t, err)
	check("dashboard charts", raw, "{campaign_views:[{count:num,date:str}],link_clicks:[{count:num,date:str}]}")
	raw, err = st.Stats().ListCharts(ctx, f.privSingle.ID, false)
	must(t, err)
	check("list charts", raw, "{campaign_views:[],subscriptions:[{count:num,date:str}],unsubscriptions:[{count:num,date:str}]}")
	raw, err = st.Stats().ListCharts(ctx, f.privSingle.ID, true)
	must(t, err)
	check("list charts (views)", raw, "{campaign_views:[{count:num,date:str}],subscriptions:[{count:num,date:str}],unsubscriptions:[{count:num,date:str}]}")

	bs, _, err := st.Bounces().Query(ctx, store.BounceFilter{Scope: all}, store.Sort{Field: "created_at"}, store.Page{Limit: 1})
	must(t, err)
	check("bounce campaign", *bs[0].Campaign, "{id:num,name:str}")

	lr, err := st.Roles().CreateListRole(ctx, auth.ListRole{Name: null.StringFrom("R"), Lists: []auth.ListPermission{{ID: f.privSingle.ID, Permissions: []string{"list:get"}}}})
	must(t, err)
	lrs, err := st.Roles().ListRoles(ctx, "", store.Sort{Field: "name"})
	must(t, err)
	check("list role lists", lrs[0].ListsRaw, "[{id:num,name:str,permissions:[str]}]")
	_ = lr
}
