package storetest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

func newCampaign(name string) models.Campaign {
	return models.Campaign{Type: "regular", Name: name, Subject: "Hello", FromEmail: "Parity <p@example.com>",
		Body: "<p>body</p>", ContentType: "richtext", Messenger: "email", Headers: models.Headers{},
		Attribs: models.JSON{}, ArchiveMeta: json.RawMessage("{}"), Tags: []string{}}
}

func campNames(cs models.Campaigns) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func testTemplates(t *testing.T, st store.Store) {
	T := st.Templates()

	camp, err := T.Create(ctx, store.TemplateInput{Name: "Camp", Type: "campaign", Body: `{{ template "content" . }}`})
	must(t, err)
	eq(t, camp.Type, "campaign", "type")
	eq(t, camp.IsDefault, false, "not default yet")
	tx, err := T.Create(ctx, store.TemplateInput{Name: "Tx", Type: "tx", Subject: "Hi {{ .Subscriber.Name }}", Body: "tx body"})
	must(t, err)
	eq(t, tx.Subject, "Hi {{ .Subscriber.Name }}", "tx subject")
	vis, err := T.Create(ctx, store.TemplateInput{Name: "Visual", Type: "campaign_visual", Body: "<p>v</p>", BodySource: null.StringFrom(`{"blocks":[]}`)})
	must(t, err)
	eq(t, vis.BodySource.Valid, true, "visual body source")

	got, err := T.Get(ctx, camp.ID, true)
	must(t, err)
	eq(t, got.Body, "", "no body")
	got, err = T.Get(ctx, camp.ID, false)
	must(t, err)
	eq(t, got.Body, `{{ template "content" . }}`, "body")
	_, err = T.Get(ctx, 9999, false)
	isErr(t, err, store.ErrNotFound, "get missing")

	got, err = T.Update(ctx, camp.ID, store.TemplateInput{Name: "Camp 2", Body: "b2"})
	must(t, err)
	eq(t, got.Name, "Camp 2", "update name")
	eq(t, got.Body, "b2", "update body")
	_, err = T.Update(ctx, 9999, store.TemplateInput{Name: "x", Body: "x"})
	isErr(t, err, store.ErrNotFound, "update missing")

	list, err := T.Query(ctx, "", "tx", false, store.Sort{Field: "name"})
	must(t, err)
	eq(t, len(list), 1, "query by type")
	list, err = T.Query(ctx, "camp", "", true, store.Sort{Field: "name"})
	must(t, err)
	eq(t, len(list), 1, "query by name")
	eq(t, list[0].Body, "", "query without body")

	// Default template: can't be deleted; campaigns on a deleted
	// template move to the default one.
	must(t, T.SetDefault(ctx, camp.ID))
	got, _ = T.Get(ctx, camp.ID, true)
	eq(t, got.IsDefault, true, "set default")
	isErr(t, T.Delete(ctx, camp.ID), store.ErrNotFound, "delete the default")
	other, err := T.Create(ctx, store.TemplateInput{Name: "Other", Type: "campaign", Body: "o"})
	must(t, err)
	c := newCampaign("on other")
	c.TemplateID = null.IntFrom(other.ID)
	cm, err := st.Campaigns().Create(ctx, c, nil, nil)
	must(t, err)
	must(t, T.Delete(ctx, other.ID))
	cm, err = st.Campaigns().Get(ctx, store.CampaignRef{ID: cm.ID}, store.TemplateCampaign)
	must(t, err)
	eq(t, cm.TemplateID.Int, camp.ID, "campaign moved to the default template")
	must(t, T.Delete(ctx, tx.ID))
}

func testMedia(t *testing.T, st store.Store) {
	M := st.Media()
	a, err := M.Insert(ctx, store.MediaInput{Filename: "logo.png", Thumb: "thumb_logo.png", ContentType: "image/png", Provider: "filesystem", Meta: models.JSON{"width": 1}, Tags: []string{"brand", "logo"}})
	must(t, err)
	eq(t, a.Tags, []string{"brand", "logo"}, "tags")
	_, err = M.Insert(ctx, store.MediaInput{Filename: "Photo.JPG", ContentType: "image/jpeg", Provider: "filesystem", Meta: models.JSON{}, Tags: []string{"photo"}})
	must(t, err)
	_, err = M.Insert(ctx, store.MediaInput{Filename: "other.png", ContentType: "image/png", Provider: "s3", Meta: models.JSON{}, Tags: []string{}})
	must(t, err)

	got, err := M.Get(ctx, store.MediaRef{UUID: a.UUID})
	must(t, err)
	eq(t, got.ID, a.ID, "get by uuid")
	got, err = M.Get(ctx, store.MediaRef{Filename: "logo.png"})
	must(t, err)
	eq(t, got.ID, a.ID, "get by filename")
	_, err = M.Get(ctx, store.MediaRef{ID: 9999})
	isErr(t, err, store.ErrNotFound, "get missing")

	out, total, err := M.Query(ctx, "filesystem", "", nil, store.Page{Limit: 10})
	must(t, err)
	eq(t, total, 2, "query by provider")
	eq(t, len(out), 2, "query by provider, rows")
	out, _, err = M.Query(ctx, "filesystem", "PHOTO", nil, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 1, "case-insensitive filename search")
	out, _, err = M.Query(ctx, "filesystem", "bran", nil, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 1, "search matches a tag")
	out, _, err = M.Query(ctx, "filesystem", "", []string{"brand", "logo"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 1, "all tags")
	out, _, err = M.Query(ctx, "filesystem", "", []string{"brand", "photo"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 0, "all tags, none match")

	tags, err := M.Tags(ctx, "filesystem")
	must(t, err)
	eq(t, len(tags), 3, "tags with counts")

	got, err = M.SetTags(ctx, a.ID, []string{"x"})
	must(t, err)
	eq(t, got.Tags, []string{"x"}, "set tags")
	_, err = M.SetTags(ctx, 9999, []string{"x"})
	isErr(t, err, store.ErrNotFound, "set tags missing")

	fname, err := M.Delete(ctx, a.ID)
	must(t, err)
	eq(t, fname, "logo.png", "delete returns filename")
}

func testCampaigns(t *testing.T, st store.Store) {
	f := seed(t, st)
	C := st.Campaigns()
	tpl, err := st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `[{{ template "content" . }}]`})
	must(t, err)
	must(t, st.Templates().SetDefault(ctx, tpl.ID))
	arch, err := st.Templates().Create(ctx, store.TemplateInput{Name: "Archive", Type: "campaign", Body: `A{{ template "content" . }}`})
	must(t, err)
	m, err := st.Media().Insert(ctx, store.MediaInput{Filename: "a.pdf", ContentType: "application/pdf", Provider: "filesystem", Meta: models.JSON{}})
	must(t, err)

	c := newCampaign("Spring sale")
	c.Tags = []string{"promo", "q2"}
	c.Archive = true
	c.ArchiveSlug = null.StringFrom("spring")
	c.ArchiveTemplateID = null.IntFrom(arch.ID)
	one, err := C.Create(ctx, c, []int{f.privSingle.ID, f.pubDouble.ID}, []int{m.ID})
	must(t, err)
	eq(t, one.Status, "draft", "new campaign is a draft")
	eq(t, one.Tags, []string{"promo", "q2"}, "tags")
	eq(t, one.TemplateID.Int, tpl.ID, "default template when none given")
	var lists []map[string]any
	must(t, json.Unmarshal(one.Lists, &lists))
	eq(t, len(lists), 2, "stats: lists loaded")
	var media []map[string]any
	must(t, json.Unmarshal(one.Media, &media))
	eq(t, len(media), 1, "stats: media loaded")
	if one.UUID == "" {
		t.Fatalf("no uuid")
	}

	two, err := C.Create(ctx, newCampaign("Autumn news"), []int{f.pubSingle.ID}, nil)
	must(t, err)
	eq(t, two.Tags, []string{}, "empty tags are []")
	c3 := newCampaign("Dup slug")
	c3.ArchiveSlug = null.StringFrom("spring")
	_, err = C.Create(ctx, c3, nil, nil)
	conflict(t, err, "archive_slug", "duplicate archive slug")

	// Get: by ID, UUID, archive slug; template bodies.
	got, err := C.Get(ctx, store.CampaignRef{UUID: one.UUID}, store.TemplateCampaign)
	must(t, err)
	eq(t, got.ID, one.ID, "get by uuid")
	eq(t, got.TemplateBody, `[{{ template "content" . }}]`, "campaign template body")
	eq(t, got.ListUUIDs, []string{f.pubDouble.UUID, f.privSingle.UUID}, "list UUIDs, by list ID")
	pv, err := C.GetForPreview(ctx, one.ID, 0)
	must(t, err)
	eq(t, pv.ListUUIDs, []string{f.pubDouble.UUID, f.privSingle.UUID}, "preview list UUIDs")
	got, err = C.Get(ctx, store.CampaignRef{ArchiveSlug: "spring"}, store.TemplateArchive)
	must(t, err)
	eq(t, got.ID, one.ID, "get by slug")
	eq(t, got.TemplateBody, `A{{ template "content" . }}`, "archive template body")
	got, err = C.Get(ctx, store.CampaignRef{ID: two.ID, ArchiveSlug: "spring"}, store.TemplateCampaign)
	must(t, err)
	eq(t, got.ID, two.ID, "id wins")
	_, err = C.Get(ctx, store.CampaignRef{ID: 9999}, store.TemplateCampaign)
	isErr(t, err, store.ErrNotFound, "get missing")
	p, err := C.GetForPreview(ctx, one.ID, 0)
	must(t, err)
	eq(t, p.TemplateBody, `[{{ template "content" . }}]`, "preview with own template")
	p, err = C.GetForPreview(ctx, one.ID, arch.ID)
	must(t, err)
	eq(t, p.TemplateBody, `A{{ template "content" . }}`, "preview with another template")
	_, err = C.GetForPreview(ctx, 9999, 0)
	isErr(t, err, store.ErrNotFound, "preview missing")

	// Update.
	u := got
	u.Name, u.Subject, u.Tags = "Autumn news 2", "Subj 2", []string{"x"}
	got, err = C.Update(ctx, two.ID, u, []int{f.pubSingle.ID, f.privSingle.ID}, nil)
	must(t, err)
	eq(t, got.Name, "Autumn news 2", "update name")
	eq(t, got.Tags, []string{"x"}, "update tags")
	must(t, json.Unmarshal(got.Lists, &lists))
	eq(t, len(lists), 2, "update lists")

	// Query.
	q := func(fl store.CampaignFilter, s store.Sort) []string {
		t.Helper()
		out, _, err := C.Query(ctx, fl, s, store.Page{Limit: 10})
		must(t, err)
		return campNames(out)
	}
	eq(t, q(store.CampaignFilter{Scope: all}, store.Sort{Field: "name"}), []string{"Autumn news 2", "Spring sale"}, "query sort by name")
	eq(t, q(store.CampaignFilter{Search: "spring", Scope: all}, store.Sort{Field: "name"}), []string{"Spring sale"}, "query search")
	eq(t, q(store.CampaignFilter{Tags: []string{"promo"}, Scope: all}, store.Sort{Field: "name"}), []string{"Spring sale"}, "query tags")
	eq(t, q(store.CampaignFilter{Statuses: []string{"running"}, Scope: all}, store.Sort{Field: "name"}), []string{}, "query status")
	eq(t, q(store.CampaignFilter{Scope: scope(f.pubDouble.ID)}, store.Sort{Field: "name"}), []string{"Spring sale"}, "query scoped")
	out, total, err := C.Query(ctx, store.CampaignFilter{Scope: all}, store.Sort{Field: "created_at"}, store.Page{Limit: 1})
	must(t, err)
	eq(t, total, 2, "query total")
	eq(t, campNames(out), []string{"Spring sale"}, "query page")

	has, err := C.HasLists(ctx, one.ID, []int{f.pubSingle.ID})
	must(t, err)
	eq(t, has, false, "has lists: no")
	has, err = C.HasLists(ctx, one.ID, []int{f.pubSingle.ID, f.pubDouble.ID})
	must(t, err)
	eq(t, has, true, "has lists: yes")

	// Archive.
	must(t, C.SetArchive(ctx, two.ID, store.ArchiveSettings{Enabled: true, Slug: "autumn", TemplateID: arch.ID, Meta: models.JSON{"name": "Reader"}}))
	arcs, total, err := C.Archived(ctx, "", store.Page{Limit: 10})
	must(t, err)
	eq(t, total, 0, "archive lists only finished/running campaigns")
	eq(t, len(arcs), 0, "archive empty")
	err = C.SetArchive(ctx, two.ID, store.ArchiveSettings{Enabled: true, Slug: "spring", TemplateID: arch.ID, Meta: models.JSON{}})
	conflict(t, err, "archive_slug", "set archive to a duplicate slug")

	// Status.
	must(t, C.SetStatus(ctx, two.ID, "cancelled"))
	got, _ = C.Get(ctx, store.CampaignRef{ID: two.ID}, store.TemplateCampaign)
	eq(t, got.Status, "cancelled", "set status")
	isErr(t, C.SetStatus(ctx, 9999, "paused"), store.ErrNotFound, "set status missing")

	// Delete.
	isErr(t, C.Delete(ctx, 9999), store.ErrNotFound, "delete missing")
	n, err := C.DeleteMany(ctx, store.CampaignFilter{Search: "autumn", Scope: scope(f.pubDouble.ID)})
	must(t, err)
	eq(t, n, 0, "delete many outside scope")
	n, err = C.DeleteMany(ctx, store.CampaignFilter{Search: "autumn", Scope: all})
	must(t, err)
	eq(t, n, 1, "delete many by search")
	must(t, C.Delete(ctx, one.ID))
	eq(t, q(store.CampaignFilter{Scope: all}, store.Sort{Field: "name"}), []string{}, "all deleted")
}

// testSending runs a campaign through Next/NextSubscribers/SetCounts the
// way internal/manager does, checking recipient selection.
func testSending(t *testing.T, st store.Store) {
	f := seed(t, st)
	C := st.Campaigns()
	tpl, err := st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `{{ template "content" . }}`})
	must(t, err)
	must(t, st.Templates().SetDefault(ctx, tpl.ID))

	// anna: pubDouble unconfirmed, privSingle unconfirmed
	// ben:  pubDouble confirmed,   privSingle confirmed (then blocklisted below)
	// cara: privSingle confirmed, then unsubscribed below
	must(t, st.Subscriptions().Unsubscribe(ctx, []int{f.cara.ID}, []int{f.privSingle.ID}, nil))
	eve, err := st.Subscribers().Insert(ctx, store.SubscriberInput{Email: "eve@example.com", Name: "Eve", Status: "enabled", Attribs: models.JSON{}},
		store.ListChange{ListIDs: []int{f.pubDouble.ID, f.privSingle.ID}, Status: "confirmed"})
	must(t, err)
	must(t, st.Subscribers().Blocklist(ctx, []int{f.ben.ID}))

	regular, err := C.Create(ctx, newCampaign("regular"), []int{f.pubDouble.ID, f.privSingle.ID}, nil)
	must(t, err)
	oc := newCampaign("optin")
	oc.Type = "optin"
	optin, err := C.Create(ctx, oc, []int{f.pubDouble.ID}, nil)
	must(t, err)
	sched := newCampaign("scheduled later")
	sched.SendAt = null.TimeFrom(time.Now().Add(time.Hour))
	later, err := C.Create(ctx, sched, []int{f.privSingle.ID}, nil)
	must(t, err)
	must(t, C.SetStatus(ctx, later.ID, "scheduled"))

	// Nothing running yet.
	next, err := C.Next(ctx, nil)
	must(t, err)
	eq(t, len(next), 0, "nothing due")

	must(t, C.SetStatus(ctx, regular.ID, "running"))
	must(t, C.SetStatus(ctx, optin.ID, "running"))
	next, err = C.Next(ctx, nil)
	must(t, err)
	eq(t, len(next), 2, "two running campaigns due")
	byID := map[int]*models.Campaign{}
	for _, c := range next {
		byID[c.ID] = c
	}
	eq(t, byID[regular.ID].ListUUIDs, []string{f.pubDouble.UUID, f.privSingle.UUID}, "next: list UUIDs")
	eq(t, byID[regular.ID].TemplateBody, `{{ template "content" . }}`, "next: template body")

	// Regular: double opt-in list -> confirmed only; single opt-in list ->
	// anything but unsubscribed; never blocklisted.
	got, err := C.Get(ctx, store.CampaignRef{ID: regular.ID}, store.TemplateCampaign)
	must(t, err)
	eq(t, got.ToSend, 2, "to_send counted (anna via single opt-in, eve)")
	subs, err := C.NextSubscribers(ctx, regular.ID, 0, 1)
	must(t, err)
	eq(t, subEmails(subs), []string{"anna@example.com"}, "batch 1, ordered by id")
	subs, err = C.NextSubscribers(ctx, regular.ID, subs[0].ID, 10)
	must(t, err)
	eq(t, subEmails(subs), []string{"eve@example.com"}, "batch 2")
	subs, err = C.NextSubscribers(ctx, regular.ID, subs[0].ID, 10)
	must(t, err)
	eq(t, len(subs), 0, "done")

	// Opt-in campaign: unconfirmed subscribers of double opt-in lists.
	subs, err = C.NextSubscribers(ctx, optin.ID, 0, 10)
	must(t, err)
	eq(t, subEmails(subs), []string{"anna@example.com"}, "optin campaign recipients")

	// Checkpoints are saved through Next; in-progress campaigns aren't returned.
	must(t, C.SetCounts(ctx, regular.ID, 2, 1, f.anna.ID))
	next, err = C.Next(ctx, []store.CampaignProgress{{ID: regular.ID, Sent: 1, LastSubID: eve.ID}, {ID: optin.ID}})
	must(t, err)
	eq(t, len(next), 0, "in-progress campaigns excluded")
	got, _ = C.Get(ctx, store.CampaignRef{ID: regular.ID}, store.TemplateCampaign)
	eq(t, got.Sent, 2, "sent counts added")
	eq(t, got.LastSubscriberID, eve.ID, "checkpoint saved")

	stats, err := C.RunningStats(ctx, all)
	must(t, err)
	eq(t, len(stats), 2, "running stats")
	stats, err = C.RunningStats(ctx, scope(f.pubSingle.ID))
	must(t, err)
	eq(t, len(stats), 0, "running stats out of scope")

	// A scheduled campaign whose send time has passed is due and starts;
	// one in the future isn't.
	past := newCampaign("scheduled past")
	past.SendAt = null.TimeFrom(time.Now().Add(-time.Minute))
	due, err := C.Create(ctx, past, []int{f.privSingle.ID}, nil)
	must(t, err)
	must(t, C.SetStatus(ctx, due.ID, "scheduled"))
	next, err = C.Next(ctx, []store.CampaignProgress{{ID: regular.ID}, {ID: optin.ID}})
	must(t, err)
	eq(t, len(next), 1, "past scheduled campaign due (future one not)")
	if len(next) == 1 {
		eq(t, next[0].ID, due.ID, "the due campaign")
	}
	got, _ = C.Get(ctx, store.CampaignRef{ID: due.ID}, store.TemplateCampaign)
	eq(t, got.Status, "running", "due scheduled campaign started")
	eq(t, got.StartedAt.Valid, true, "started_at set")
	got, _ = C.Get(ctx, store.CampaignRef{ID: later.ID}, store.TemplateCampaign)
	eq(t, got.Status, "scheduled", "future campaign still scheduled")

	// A campaign with no lists has no recipients.
	empty, err := C.Create(ctx, newCampaign("empty"), nil, nil)
	must(t, err)
	subs, err = C.NextSubscribers(ctx, empty.ID, 0, 10)
	must(t, err)
	eq(t, len(subs), 0, "no lists, no recipients")
}

func testTracking(t *testing.T, st store.Store) {
	f := seed(t, st)
	T := st.Tracking()
	camp, err := st.Campaigns().Create(ctx, newCampaign("c"), []int{f.privSingle.ID}, nil)
	must(t, err)

	l1, err := T.CreateLink(ctx, "https://example.com/a")
	must(t, err)
	l2, err := T.CreateLink(ctx, "https://example.com/a")
	must(t, err)
	eq(t, l2, l1, "create link is idempotent")
	url, err := T.LinkURL(ctx, l1)
	must(t, err)
	eq(t, url, "https://example.com/a", "link url")
	_, err = T.LinkURL(ctx, "00000000-0000-0000-0000-000000000000")
	isErr(t, err, store.ErrNotFound, "unknown link")

	must(t, T.RegisterView(ctx, camp.UUID, f.anna.UUID))
	must(t, T.RegisterView(ctx, camp.UUID, ""))
	isErr(t, T.RegisterView(ctx, "00000000-0000-0000-0000-000000000000", f.anna.UUID), store.ErrNotFound, "view of an unknown campaign")
	url, err = T.RegisterClick(ctx, l1, camp.UUID, f.anna.UUID)
	must(t, err)
	eq(t, url, "https://example.com/a", "click returns url")
	_, err = T.RegisterClick(ctx, "00000000-0000-0000-0000-000000000000", camp.UUID, f.anna.UUID)
	isErr(t, err, store.ErrNotFound, "click on an unknown link")

	views, err := T.ExportViews(ctx, time.Now().Add(-time.Hour), store.Page{Limit: 10})
	must(t, err)
	eq(t, len(views), 2, "export views")
	clicks, err := T.ExportClicks(ctx, time.Now().Add(-time.Hour), store.Page{Limit: 10})
	must(t, err)
	eq(t, len(clicks), 1, "export clicks")
	views, err = T.ExportViews(ctx, time.Now().Add(-time.Hour), store.Page{Offset: 1, Limit: 10})
	must(t, err)
	eq(t, len(views), 1, "export views, offset")

	a, err := st.Subscribers().Activity(ctx, f.anna.ID, all)
	must(t, err)
	if string(a.CampaignViews) == "[]" || len(a.CampaignViews) == 0 {
		t.Errorf("subscriber activity: no views: %s", a.CampaignViews)
	}

	n, err := T.DeleteViewsBefore(ctx, time.Now().Add(-time.Hour))
	must(t, err)
	eq(t, n, 0, "delete old views: none")
	n, err = T.DeleteViewsBefore(ctx, time.Now().Add(time.Hour))
	must(t, err)
	eq(t, n, 2, "delete views")
	n, err = T.DeleteClicksBefore(ctx, time.Now().Add(time.Hour))
	must(t, err)
	eq(t, n, 1, "delete clicks")
}

func testBounces(t *testing.T, st store.Store) {
	f := seed(t, st)
	B := st.Bounces()
	camp, err := st.Campaigns().Create(ctx, newCampaign("c"), []int{f.privSingle.ID}, nil)
	must(t, err)
	none := store.BounceAction{Count: 2, Action: "none"}
	block := store.BounceAction{Count: 2, Action: "blocklist"}

	rec := func(email, uuid, typ string, a store.BounceAction) error {
		return B.Record(ctx, models.Bounce{Email: email, SubscriberUUID: uuid, CampaignUUID: camp.UUID, Type: typ, Source: "api",
			Meta: json.RawMessage(`{"r":1}`), CreatedAt: time.Now()}, a)
	}
	must(t, rec("cara@example.com", "", "soft", none))
	must(t, rec("", f.cara.UUID, "soft", none))
	must(t, rec("ben@example.com", "", "hard", block))
	must(t, rec("ben@example.com", "", "hard", block))
	isErr(t, rec("nobody@example.com", "", "hard", block), store.ErrNotFound, "unknown subscriber")

	s, _ := st.Subscribers().Get(ctx, store.SubscriberRef{ID: f.ben.ID})
	eq(t, s.Status, "blocklisted", "blocklisted at the threshold")
	s, _ = st.Subscribers().Get(ctx, store.SubscriberRef{ID: f.cara.ID})
	eq(t, s.Status, "enabled", "action none")
	must(t, rec("ben@example.com", "", "hard", block))

	out, total, err := B.Query(ctx, store.BounceFilter{Scope: all}, store.Sort{Field: "email"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, total, 4, "bounces stored (none after blocklisting)")
	eq(t, out[0].Email, "ben@example.com", "sorted by email")
	out, _, err = B.Query(ctx, store.BounceFilter{SubscriberID: f.cara.ID, Type: "soft", Scope: all}, store.Sort{Field: "created_at"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 2, "filter by subscriber and type")
	out, _, err = B.Query(ctx, store.BounceFilter{Scope: scope(f.pubSingle.ID)}, store.Sort{Field: "created_at"}, store.Page{Limit: 10})
	must(t, err)
	eq(t, len(out), 0, "scoped")
	b, err := B.Get(ctx, out0(t, B).ID, all)
	must(t, err)
	if b.Email == "" {
		t.Errorf("get bounce: no email")
	}
	_, err = B.Get(ctx, 9999, all)
	isErr(t, err, store.ErrNotFound, "get missing")

	// Blocklist by bounces: nothing without IDs or All.
	must(t, B.BlocklistSubscribers(ctx, store.BounceFilter{Scope: all}))
	s, _ = st.Subscribers().Get(ctx, store.SubscriberRef{ID: f.cara.ID})
	eq(t, s.Status, "enabled", "nothing selected")
	must(t, B.BlocklistSubscribers(ctx, store.BounceFilter{All: true, Type: "soft", Scope: all}))
	s, _ = st.Subscribers().Get(ctx, store.SubscriberRef{ID: f.cara.ID})
	eq(t, s.Status, "blocklisted", "blocklist by bounce filter")

	// Delete.
	n, err := B.Delete(ctx, store.BounceFilter{Scope: all})
	must(t, err)
	eq(t, n, 0, "delete nothing without IDs or All")
	n, err = B.Delete(ctx, store.BounceFilter{IDs: []int{b.ID}, Scope: all})
	must(t, err)
	eq(t, n, 1, "delete by id")
	must(t, B.DeleteForSubscriber(ctx, store.SubscriberRef{ID: f.cara.ID}))
	n, err = B.Delete(ctx, store.BounceFilter{All: true, Scope: all})
	must(t, err)
	eq(t, n, 2, "delete all remaining (ben's two)")
}

func out0(t *testing.T, B store.BounceStore) models.Bounce {
	t.Helper()
	out, _, err := B.Query(ctx, store.BounceFilter{Scope: all}, store.Sort{Field: "created_at"}, store.Page{Limit: 1})
	must(t, err)
	if len(out) == 0 {
		t.Fatalf("no bounces")
	}
	return out[0]
}
