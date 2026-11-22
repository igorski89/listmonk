// Package storetest is the conformance suite for internal/store: one set of
// tests every backend must pass. Postgres (running upstream's queries) is
// the reference; the suite pins down its observable behaviour so that other
// backends can be checked against it.
//
// A backend's test package calls Run with an Opener that returns a freshly
// installed, configured store, and the backend's SQL expression fixtures.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

// Opener returns a freshly installed store (empty schema, default
// settings, no sample data), configured with o.
type Opener func(t *testing.T, o store.Options) store.Store

// Fixtures are a backend's SQL expressions for SubscriberFilter.Expr.
type Fixtures struct {
	// CityBerlin selects subscribers whose attribs.city is "Berlin".
	CityBerlin string
	// ForbiddenTable reads a table outside the allowed set (users).
	ForbiddenTable string
	// Write attempts a write (a second statement).
	Write string
}

var ctx = context.Background()

// Run runs the whole suite.
func Run(t *testing.T, open Opener, fx Fixtures) {
	def := store.Options{SessionTTL: 3600e9}

	t.Run("Schema", func(t *testing.T) { testSchema(t, open(t, def)) })
	t.Run("Settings", func(t *testing.T) { testSettings(t, open(t, def)) })
	t.Run("Lists", func(t *testing.T) { testLists(t, open(t, def)) })
	t.Run("Subscribers", func(t *testing.T) { testSubscribers(t, open(t, def), fx) })
	t.Run("SubscriberFilters", func(t *testing.T) { testSubscriberFilters(t, open(t, def), fx) })
	t.Run("Subscriptions", func(t *testing.T) { testSubscriptions(t, open(t, def), fx) })
	t.Run("Import", func(t *testing.T) { testImport(t, open(t, def)) })
	t.Run("Templates", func(t *testing.T) { testTemplates(t, open(t, def)) })
	t.Run("Media", func(t *testing.T) { testMedia(t, open(t, def)) })
	t.Run("Campaigns", func(t *testing.T) { testCampaigns(t, open(t, def)) })
	t.Run("Sending", func(t *testing.T) { testSending(t, open(t, def)) })
	t.Run("Tracking", func(t *testing.T) { testTracking(t, open(t, def)) })
	t.Run("Bounces", func(t *testing.T) { testBounces(t, open(t, def)) })
	t.Run("Users", func(t *testing.T) { testUsers(t, open(t, def)) })
	t.Run("Roles", func(t *testing.T) { testRoles(t, open(t, def)) })
	t.Run("Sessions", func(t *testing.T) { testSessions(t, open(t, def)) })
	for _, o := range []store.Options{
		{SessionTTL: 3600e9},
		{SessionTTL: 3600e9, IndividualTracking: true},
		{SessionTTL: 3600e9, CacheSlowQueries: true},
	} {
		o := o
		t.Run(fmt.Sprintf("Analytics/tracking=%v,cache=%v", o.IndividualTracking, o.CacheSlowQueries), func(t *testing.T) {
			testAnalytics(t, open(t, o), o)
		})
	}
}

// Helpers.

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func eq[T any](t *testing.T, got, want T, what string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", what, got, want)
	}
}

func isErr(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: got error %v, want %v", what, err, target)
	}
}

func conflict(t *testing.T, err error, field, what string) {
	t.Helper()
	var ce *store.ConflictError
	if !errors.As(err, &ce) {
		t.Errorf("%s: got error %v, want *store.ConflictError{%s}", what, err, field)
		return
	}
	eq(t, ce.Field, field, what+": conflict field")
}

func invalidQuery(t *testing.T, err error, what string) {
	t.Helper()
	var ie *store.InvalidQueryError
	if !errors.As(err, &ie) {
		t.Errorf("%s: got error %v, want *store.InvalidQueryError", what, err)
	}
}

var all = store.ListScope{All: true}

func scope(ids ...int) store.ListScope { return store.ListScope{IDs: ids} }

// fixture holds a small, known data set.
type fixture struct {
	pubDouble, privSingle, pubSingle models.List
	anna, ben, cara, dan             models.Subscriber
}

// seed creates three lists and four subscribers:
//
//	pubDouble (public, double opt-in):  anna unconfirmed, ben confirmed
//	privSingle (private, single):       anna, ben, cara (confirmed)
//	pubSingle (public, single):         none
//	dan: no lists
func seed(t *testing.T, st store.Store) fixture {
	t.Helper()
	var f fixture
	var err error
	f.pubDouble, err = st.Lists().Create(ctx, models.List{Name: "Weekly news", Type: "public", Optin: "double", Status: "active", Tags: []string{"news", "weekly"}, Description: "weekly"})
	must(t, err)
	f.privSingle, err = st.Lists().Create(ctx, models.List{Name: "Customers", Type: "private", Optin: "single", Status: "active", Tags: []string{"crm"}})
	must(t, err)
	f.pubSingle, err = st.Lists().Create(ctx, models.List{Name: "Announcements", Type: "public", Optin: "single", Status: "active", Tags: []string{}})
	must(t, err)

	f.anna, err = st.Subscribers().Insert(ctx, store.SubscriberInput{Email: "anna@example.com", Name: "Anna", Status: "enabled", Attribs: models.JSON{"city": "Berlin", "n": 3}},
		store.ListChange{ListIDs: []int{f.pubDouble.ID, f.privSingle.ID}, Status: "unconfirmed"})
	must(t, err)
	f.ben, err = st.Subscribers().Insert(ctx, store.SubscriberInput{Email: "ben@example.com", Name: "Ben", Status: "enabled", Attribs: models.JSON{"city": "Paris"}},
		store.ListChange{ListIDs: []int{f.pubDouble.ID, f.privSingle.ID}, Status: "confirmed"})
	must(t, err)
	f.cara, err = st.Subscribers().Insert(ctx, store.SubscriberInput{Email: "cara@example.com", Name: "Cara", Status: "enabled", Attribs: models.JSON{}},
		store.ListChange{ListIDs: []int{f.privSingle.ID}, Status: "confirmed"})
	must(t, err)
	f.dan, err = st.Subscribers().Insert(ctx, store.SubscriberInput{Email: "dan@example.com", Name: "Dan", Status: "enabled", Attribs: models.JSON{"city": "Berlin"}},
		store.ListChange{Status: "unconfirmed"})
	must(t, err)
	return f
}

// subStatuses returns a subscriber's subscriptions as list ID -> status.
func subStatuses(t *testing.T, st store.Store, id int) map[int]string {
	t.Helper()
	s, err := st.Subscribers().Get(ctx, store.SubscriberRef{ID: id})
	must(t, err)
	out := map[int]string{}
	for _, l := range s.ParsedLists() {
		out[l.ID] = l.SubscriptionStatus
	}
	return out
}

func subEmails(subs []models.Subscriber) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Email)
	}
	return out
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func listNames(ls []models.List) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}
