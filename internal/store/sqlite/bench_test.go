package sqlite_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/sqlite"
	"github.com/knadh/listmonk/models"
)

// TestScale times the operations a send and the admin UI do on a large
// list. Opt-in: LISTMONK_TEST_SCALE=100000 go test -run TestScale -v
func TestScale(t *testing.T) {
	n := 0
	fmt.Sscanf(os.Getenv("LISTMONK_TEST_SCALE"), "%d", &n)
	if n == 0 {
		t.Skip("LISTMONK_TEST_SCALE not set")
	}
	ctx := context.Background()
	lo := log.New(io.Discard, "", 0)
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "scale.db"), lo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Schema().Install(ctx, "v7.0.0", store.MigrationEnv{Log: lo}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configure(store.Options{}); err != nil {
		t.Fatal(err)
	}
	timed := func(what string, fn func() error) {
		t0 := time.Now()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%-40s %v", what, time.Since(t0).Round(time.Millisecond))
	}

	l, _ := st.Lists().Create(ctx, models.List{Name: "big", Type: "private", Optin: "single", Status: "active"})
	_, _ = st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `{{ template "content" . }}`})
	timed(fmt.Sprintf("import %d subscribers (10k batches)", n), func() error {
		for i := 0; i < n; i += 10000 {
			rows := make([]store.SubscriberInput, 0, 10000)
			for j := i; j < i+10000 && j < n; j++ {
				rows = append(rows, store.SubscriberInput{Email: fmt.Sprintf("u%d@example.com", j), Name: fmt.Sprintf("User %d", j),
					Attribs: models.JSON{"city": []string{"Berlin", "Paris", "Rome"}[j%3]}})
			}
			if err := st.Subscribers().Import(ctx, rows, store.ImportOptions{Mode: store.ImportSubscribe, ListIDs: []int{l.ID},
				SubscriptionStatus: "confirmed", Permitted: store.ListScope{All: true}}); err != nil {
				return err
			}
		}
		return nil
	})
	timed("query page 1 (all)", func() error {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	timed("query page 1 (list)", func() error {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{ListIDs: []int{l.ID}}, store.Sort{Field: "email"}, store.Page{Limit: 20})
		return err
	})
	timed("query search", func() error {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Search: "user 4242"}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	timed("query SQL expression", func() error {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Expr: "subscribers.attribs ->> 'city' = 'Rome'"}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	timed("lists with counts", func() error {
		_, _, err := st.Lists().Query(ctx, store.ListFilter{Scope: store.ListScope{All: true}}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	timed("dashboard counts", func() error { _, err := st.Stats().DashboardCounts(ctx); return err })

	c := models.Campaign{Type: "regular", Name: "big", Subject: "s", FromEmail: "a@b.c", Body: "b", ContentType: "richtext",
		Messenger: "email", Headers: models.Headers{}, Attribs: models.JSON{}}
	camp, err := st.Campaigns().Create(ctx, c, []int{l.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Campaigns().SetStatus(ctx, camp.ID, "running")
	timed("next campaigns (count recipients)", func() error { _, err := st.Campaigns().Next(ctx, nil); return err })
	timed(fmt.Sprintf("fetch all recipients (1000/batch)"), func() error {
		last, total := 0, 0
		for {
			subs, err := st.Campaigns().NextSubscribers(ctx, camp.ID, last, 1000)
			if err != nil {
				return err
			}
			if len(subs) == 0 {
				break
			}
			total += len(subs)
			last = subs[len(subs)-1].ID
		}
		if total != n {
			return fmt.Errorf("got %d recipients, want %d", total, n)
		}
		return nil
	})
	timed("1000 view registrations", func() error {
		subs, _ := st.Campaigns().NextSubscribers(ctx, camp.ID, 0, 1000)
		for _, s := range subs {
			if err := st.Tracking().RegisterView(ctx, camp.UUID, s.UUID); err != nil {
				return err
			}
		}
		return nil
	})
}
