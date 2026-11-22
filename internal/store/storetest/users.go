package storetest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"golang.org/x/crypto/bcrypt"
	null "gopkg.in/volatiletech/null.v6"
)

// superAdmin creates the Super Admin role (ID 1) and an admin user, as
// the installer does.
func superAdmin(t *testing.T, st store.Store) (auth.Role, int) {
	t.Helper()
	role, err := st.Roles().CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Super Admin"), Permissions: []string{"users:manage", "lists:get_all"}})
	must(t, err)
	eq(t, role.ID, auth.SuperAdminRoleID, "super admin role id")
	id, err := st.Users().Create(ctx, auth.User{Username: "admin", PasswordLogin: true, Password: null.StringFrom("adminpass123"),
		Email: null.StringFrom("admin@example.com"), Name: "Admin", Type: "user", UserRoleID: role.ID, Status: "enabled"})
	must(t, err)
	return role, id
}

func testUsers(t *testing.T, st store.Store) {
	U := st.Users()
	role, adminID := superAdmin(t, st)
	f := seed(t, st)
	lr, err := st.Roles().CreateListRole(ctx, auth.ListRole{Name: null.StringFrom("Newsroom"), Lists: []auth.ListPermission{{ID: f.pubDouble.ID, Permissions: []string{"list:get", "list:manage"}}}})
	must(t, err)

	u, err := U.Get(ctx, store.UserRef{ID: adminID})
	must(t, err)
	eq(t, u.Username, "admin", "get by id")
	eq(t, u.UserRolePerms, []string{"users:manage", "lists:get_all"}, "role permissions")
	if u.Password.String == "adminpass123" || u.Password.String == "" {
		t.Errorf("password stored in plain text or not at all")
	}
	// Every backend stores standard bcrypt, so a database moved between
	// backends keeps working logins.
	if err := bcrypt.CompareHashAndPassword([]byte(u.Password.String), []byte("adminpass123")); err != nil {
		t.Errorf("password hash isn't bcrypt of the password: %v", err)
	}

	// Login: right and wrong passwords; disabled users can't.
	u, err = U.Login(ctx, "admin", "adminpass123")
	must(t, err)
	eq(t, u.ID, adminID, "login")
	_, err = U.Login(ctx, "admin", "wrong")
	isErr(t, err, store.ErrNotFound, "wrong password")
	_, err = U.Login(ctx, "nobody", "adminpass123")
	isErr(t, err, store.ErrNotFound, "unknown user")

	// A second user with a list role.
	edID, err := U.Create(ctx, auth.User{Username: "editor", PasswordLogin: true, Password: null.StringFrom("editorpass123"),
		Email: null.StringFrom("ed@example.com"), Name: "Ed", Type: "user", UserRoleID: role.ID, ListRoleID: &lr.ID, Status: "enabled"})
	must(t, err)
	_, err = U.Create(ctx, auth.User{Username: "editor", PasswordLogin: true, Password: null.StringFrom("x12345678"),
		Email: null.StringFrom("ed2@example.com"), Name: "Ed2", Type: "user", UserRoleID: role.ID, Status: "enabled"})
	conflict(t, err, "username", "duplicate username")
	u, err = U.Get(ctx, store.UserRef{Username: "editor"})
	must(t, err)
	eq(t, u.ID, edID, "get by username")
	eq(t, *u.ListRoleID, lr.ID, "list role")
	var perms []auth.ListPermission
	must(t, json.Unmarshal(*u.ListsPermsRaw, &perms))
	eq(t, len(perms), 1, "list role permissions")
	u, err = U.Get(ctx, store.UserRef{Email: "ed@example.com"})
	must(t, err)
	eq(t, u.ID, edID, "get by email")
	_, err = U.Get(ctx, store.UserRef{ID: 9999})
	isErr(t, err, store.ErrNotFound, "get missing")

	users, err := U.Query(ctx, store.UserFilter{}, store.Sort{Field: "username"})
	must(t, err)
	eq(t, len(users), 2, "query")
	eq(t, users[0].Username, "admin", "sorted")
	users, err = U.Query(ctx, store.UserFilter{ListRoleID: lr.ID}, store.Sort{Field: "username"})
	must(t, err)
	eq(t, len(users), 1, "query by list role")

	// API user: the token is stored as given (a hash).
	apiID, err := U.Create(ctx, auth.User{Username: "api", Password: null.StringFrom("tokenhash"), Email: null.StringFrom("api@api"),
		Name: "api", Type: "api", UserRoleID: role.ID, Status: "enabled"})
	must(t, err)
	u, _ = U.Get(ctx, store.UserRef{ID: apiID})
	eq(t, u.Password.String, "tokenhash", "api token stored as given")

	// Update: empty fields kept; nil list role removes it; password changes.
	must(t, U.Update(ctx, edID, auth.User{Name: "Ed B", PasswordLogin: true, Password: null.StringFrom("newpass1234"), Type: "user", UserRoleID: role.ID, Status: "enabled"}))
	u, _ = U.Get(ctx, store.UserRef{ID: edID})
	eq(t, u.Name, "Ed B", "update name")
	eq(t, u.Username, "editor", "username kept")
	eq(t, u.ListRoleID == nil, true, "list role removed")
	_, err = U.Login(ctx, "editor", "newpass1234")
	must(t, err)

	// The last super admin can't be disabled, demoted or deleted.
	other, err := st.Roles().CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Other"), Permissions: []string{"lists:get_all"}})
	must(t, err)
	must(t, U.Update(ctx, edID, auth.User{PasswordLogin: true, Type: "user", UserRoleID: other.ID, Status: "enabled"}))
	err = U.Update(ctx, adminID, auth.User{PasswordLogin: true, Type: "user", UserRoleID: other.ID, Status: "enabled"})
	isErr(t, err, store.ErrLastSuperAdmin, "demote the last super admin")
	err = U.Update(ctx, adminID, auth.User{PasswordLogin: true, Type: "user", UserRoleID: role.ID, Status: "disabled"})
	isErr(t, err, store.ErrLastSuperAdmin, "disable the last super admin")
	isErr(t, U.Update(ctx, 9999, auth.User{PasswordLogin: true, Type: "user", UserRoleID: role.ID, Status: "enabled"}), store.ErrNotFound, "update missing")
	_, err = U.Delete(ctx, []int{adminID})
	isErr(t, err, store.ErrLastSuperAdmin, "delete the last super admin")

	// Profile.
	must(t, U.UpdateProfile(ctx, edID, auth.User{Name: "Eddie", Email: null.StringFrom("eddie@example.com"), PasswordLogin: true, Password: null.StringFrom("")}))
	u, _ = U.Get(ctx, store.UserRef{ID: edID})
	eq(t, u.Name, "Eddie", "profile name")
	eq(t, u.Email.String, "eddie@example.com", "profile email")
	_, err = U.Login(ctx, "editor", "newpass1234")
	must(t, err)
	isErr(t, U.UpdateProfile(ctx, 9999, auth.User{Name: "x", PasswordLogin: true}), store.ErrNotFound, "profile missing")

	must(t, U.RecordLogin(ctx, edID, "https://example.com/a.png"))
	u, _ = U.Get(ctx, store.UserRef{ID: edID})
	eq(t, u.Avatar.String, "https://example.com/a.png", "avatar")
	if !u.LoggedInAt.Valid {
		t.Errorf("login time not recorded")
	}
	must(t, U.SetTwoFA(ctx, edID, "totp", "SECRET"))
	u, _ = U.Get(ctx, store.UserRef{ID: edID})
	eq(t, u.TwofaType, "totp", "2fa type")
	eq(t, u.TwofaKey.String, "SECRET", "2fa key")

	n, err := U.Delete(ctx, []int{edID, apiID})
	must(t, err)
	eq(t, n, 2, "delete users")
}

func testRoles(t *testing.T, st store.Store) {
	R := st.Roles()
	role, _ := superAdmin(t, st)
	f := seed(t, st)

	ed, err := R.CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Editors"), Permissions: []string{"campaigns:get_all"}})
	must(t, err)
	_, err = R.CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Editors"), Permissions: []string{}})
	conflict(t, err, "name", "duplicate role name")
	ed, err = R.UpdateUserRole(ctx, ed.ID, auth.Role{Name: null.StringFrom("Editors 2"), Permissions: []string{"campaigns:get_all", "lists:get_all"}})
	must(t, err)
	eq(t, ed.Permissions, []string{"campaigns:get_all", "lists:get_all"}, "update permissions")
	_, err = R.UpdateUserRole(ctx, 9999, auth.Role{Name: null.StringFrom("x")})
	isErr(t, err, store.ErrNotFound, "update missing role")
	got, err := R.UserRole(ctx, ed.ID)
	must(t, err)
	eq(t, got.Name.String, "Editors 2", "get role")
	_, err = R.UserRole(ctx, 9999)
	isErr(t, err, store.ErrNotFound, "get missing role")
	rs, err := R.UserRoles(ctx, "", store.Sort{Field: "name"})
	must(t, err)
	eq(t, len(rs), 2, "user roles")
	rs, err = R.UserRoles(ctx, "edit", store.Sort{Field: "name"})
	must(t, err)
	eq(t, len(rs), 1, "user roles search")

	// List roles with per-list permissions, replaced on update.
	lr, err := R.CreateListRole(ctx, auth.ListRole{Name: null.StringFrom("Newsroom"), Lists: []auth.ListPermission{
		{ID: f.pubDouble.ID, Permissions: []string{"list:get", "list:manage"}},
		{ID: f.privSingle.ID, Permissions: []string{"list:get"}},
	}})
	must(t, err)
	lrs, err := R.ListRoles(ctx, "", store.Sort{Field: "name"})
	must(t, err)
	eq(t, len(lrs), 1, "list roles")
	eq(t, len(lrs[0].Lists), 2, "list role lists")
	_, err = R.UpdateListRole(ctx, lr.ID, auth.ListRole{Name: null.StringFrom("Newsroom 2"), Lists: []auth.ListPermission{
		{ID: f.privSingle.ID, Permissions: []string{"list:manage"}},
	}})
	must(t, err)
	lrs, _ = R.ListRoles(ctx, "", store.Sort{Field: "name"})
	eq(t, lrs[0].Name.String, "Newsroom 2", "list role renamed")
	eq(t, len(lrs[0].Lists), 1, "list role lists replaced")
	eq(t, lrs[0].Lists[0].Permissions, []string{"list:manage"}, "list permission")
	_, err = R.UpdateListRole(ctx, 9999, auth.ListRole{Name: null.StringFrom("x")})
	isErr(t, err, store.ErrNotFound, "update missing list role")
	must(t, R.DeleteListPermission(ctx, lr.ID, f.privSingle.ID))
	lrs, _ = R.ListRoles(ctx, "", store.Sort{Field: "name"})
	eq(t, len(lrs[0].Lists), 0, "list permission deleted")

	// Roles in use can't be deleted; others can, and missing is a no-op.
	_, err = st.Users().Create(ctx, auth.User{Username: "ed", PasswordLogin: true, Password: null.StringFrom("edpass1234"),
		Email: null.StringFrom("ed@example.com"), Name: "Ed", Type: "user", UserRoleID: ed.ID, Status: "enabled"})
	must(t, err)
	isErr(t, R.Delete(ctx, ed.ID), store.ErrInUse, "delete a role in use")
	must(t, R.Delete(ctx, lr.ID))
	must(t, R.Delete(ctx, 9999))
	_ = role
}

func testSessions(t *testing.T, st store.Store) {
	S := st.Sessions()
	must(t, S.Create("sess1"))
	must(t, S.Set("sess1", "user_id", 7))
	must(t, S.SetMulti("sess1", map[string]any{"a": "b"}))
	v, err := S.Int(S.Get("sess1", "user_id"))
	must(t, err)
	eq(t, v, 7, "session int")
	all, err := S.GetAll("sess1")
	must(t, err)
	eq(t, len(all), 2, "session get all")
	must(t, S.Delete("sess1", "a"))
	m, err := S.GetMulti("sess1", "a", "user_id")
	must(t, err)
	eq(t, m["a"], nil, "deleted key")

	// Users.DeleteSessions logs a user out except one session.
	must(t, S.Create("sess2"))
	must(t, S.Set("sess2", "user_id", 7))
	must(t, st.Users().DeleteSessions(ctx, 7, "sess2"))
	_, err = S.Get("sess1", "user_id")
	if err == nil {
		t.Errorf("session 1 still valid after DeleteSessions")
	}
	_, err = S.Get("sess2", "user_id")
	must(t, err)
	must(t, S.Destroy("sess2"))
	must(t, S.Prune())
}

func testAnalytics(t *testing.T, st store.Store, o store.Options) {
	f := seed(t, st)
	_, err := st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `{{ template "content" . }}`})
	must(t, err)
	camp, err := st.Campaigns().Create(ctx, newCampaign("c"), []int{f.privSingle.ID}, nil)
	must(t, err)
	link, err := st.Tracking().CreateLink(ctx, "https://example.com/x")
	must(t, err)
	for _, s := range []models.Subscriber{f.anna, f.anna, f.ben} {
		must(t, st.Tracking().RegisterView(ctx, camp.UUID, s.UUID))
		_, err := st.Tracking().RegisterClick(ctx, link, camp.UUID, s.UUID)
		must(t, err)
	}
	must(t, st.Bounces().Record(ctx, models.Bounce{Email: "cara@example.com", CampaignUUID: camp.UUID, Type: "soft", Source: "api",
		Meta: json.RawMessage("{}"), CreatedAt: time.Now()}, store.BounceAction{Count: 9, Action: "none"}))
	must(t, st.Campaigns().SetStatus(ctx, camp.ID, "finished"))
	if o.CacheSlowQueries {
		must(t, st.Stats().Refresh(ctx))
	}

	A := st.Analytics()
	from, to := time.Now().Add(-24*time.Hour).Format(time.RFC3339), time.Now().Add(time.Hour).Format(time.RFC3339)
	sum := func(subject store.AnalyticsSubject, id int, metric string) int {
		t.Helper()
		out, err := A.Counts(ctx, store.AnalyticsQuery{Subject: subject, IDs: []int{id}, Metric: metric, From: from, To: to, Interval: "day"})
		must(t, err)
		n := 0
		for _, c := range out {
			n += c.Count
		}
		return n
	}
	uniq := 3
	if o.IndividualTracking {
		uniq = 2
	}
	eq(t, sum(store.AnalyticsCampaigns, camp.ID, "views"), uniq, "campaign views (unique with individual tracking)")
	eq(t, sum(store.AnalyticsCampaigns, camp.ID, "clicks"), uniq, "campaign clicks")
	eq(t, sum(store.AnalyticsCampaigns, camp.ID, "total_views"), 3, "campaign total views")
	eq(t, sum(store.AnalyticsCampaigns, camp.ID, "total_clicks"), 3, "campaign total clicks")
	eq(t, sum(store.AnalyticsCampaigns, camp.ID, "bounces"), 1, "campaign bounces")
	eq(t, sum(store.AnalyticsLists, f.privSingle.ID, "views"), uniq, "list views")
	eq(t, sum(store.AnalyticsLists, f.privSingle.ID, "total_clicks"), 3, "list total clicks")
	eq(t, sum(store.AnalyticsLists, f.privSingle.ID, "subscriptions"), 3, "list subscriptions")
	eq(t, sum(store.AnalyticsLists, f.privSingle.ID, "unsubscriptions"), 0, "list unsubscriptions")
	eq(t, sum(store.AnalyticsLists, f.privSingle.ID, "bounces"), 1, "list bounces")
	_, err = A.Counts(ctx, store.AnalyticsQuery{Subject: store.AnalyticsCampaigns, IDs: []int{camp.ID}, Metric: "nonsense", From: from, To: to, Interval: "day"})
	if err == nil {
		t.Errorf("unknown metric: want an error")
	}

	links, err := A.Links(ctx, store.AnalyticsCampaigns, []int{camp.ID}, from, to)
	must(t, err)
	eq(t, len(links), 1, "campaign links")
	eq(t, links[0].Count, uniq, "link count")
	links, err = A.Links(ctx, store.AnalyticsLists, []int{f.privSingle.ID}, from, to)
	must(t, err)
	eq(t, len(links), 1, "list links")

	items, err := A.Items(ctx, store.AnalyticsCampaigns, []int{camp.ID}, all)
	must(t, err)
	eq(t, len(items), 1, "campaign items")
	items, err = A.Items(ctx, store.AnalyticsLists, []int{f.privSingle.ID, f.pubSingle.ID}, scope(f.pubSingle.ID))
	must(t, err)
	eq(t, len(items), 1, "list items, scoped")

	act, err := A.Activity(ctx, []int{camp.ID}, from, to, all, store.Page{Limit: 10})
	must(t, err)
	if len(act) == 0 {
		t.Errorf("activity: empty")
	}
	ev, err := A.ActivityEvents(ctx, []int{camp.ID}, from, to, f.anna.ID, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(ev), 4, "activity events for anna (2 views, 2 clicks)")

	// Stats JSON shapes.
	var counts map[string]any
	raw, err := st.Stats().DashboardCounts(ctx)
	must(t, err)
	must(t, json.Unmarshal(raw, &counts))
	for _, k := range []string{"subscribers", "lists", "campaigns", "messages"} {
		if _, ok := counts[k]; !ok {
			t.Errorf("dashboard counts: missing %q in %s", k, raw)
		}
	}
	subsCounts, _ := counts["subscribers"].(map[string]any)
	eq(t, subsCounts["total"], any(float64(4)), "dashboard: subscribers total")
	raw, err = st.Stats().DashboardCharts(ctx)
	must(t, err)
	if !json.Valid(raw) {
		t.Errorf("dashboard charts: not JSON")
	}
	raw, err = st.Stats().ListCharts(ctx, f.privSingle.ID, false)
	must(t, err)
	if !json.Valid(raw) {
		t.Errorf("list charts: not JSON")
	}
	raw, err = st.Stats().ListCharts(ctx, f.privSingle.ID, true)
	must(t, err)
	if !json.Valid(raw) {
		t.Errorf("list charts (views): not JSON")
	}
	must(t, st.Stats().Refresh(ctx, store.StatsListSubscribers))
}
