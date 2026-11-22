package sqlite_test

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/sqlite"
	"github.com/knadh/listmonk/internal/store/storetest"
)

// TestStore runs the conformance suite on a fresh SQLite file per test.
func TestStore(t *testing.T) {
	lo := log.New(io.Discard, "", 0)
	open := func(t *testing.T, o store.Options) store.Store {
		t.Helper()
		st, err := sqlite.Open(filepath.Join(t.TempDir(), "listmonk.db"), lo)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Schema().Install(context.Background(), "v7.0.0", store.MigrationEnv{Log: lo}); err != nil {
			t.Fatal(err)
		}
		if err := st.Configure(o); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}

	storetest.Run(t, open, storetest.Fixtures{
		CityBerlin:     "subscribers.attribs ->> 'city' = 'Berlin'",
		ForbiddenTable: "(SELECT COUNT(*) FROM users) > 0",
		Write:          "1=1; DELETE FROM subscribers",
	})
}

// TestSandbox tries to escape the SQL expression sandbox.
func TestSandbox(t *testing.T) {
	lo := log.New(io.Discard, "", 0)
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "listmonk.db"), lo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Schema().Install(ctx, "v7.0.0", store.MigrationEnv{Log: lo}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configure(store.Options{}); err != nil {
		t.Fatal(err)
	}

	for _, expr := range []string{
		"(SELECT COUNT(*) FROM users) > 0",
		"EXISTS (SELECT 1 FROM sessions)",
		"EXISTS (SELECT 1 FROM settings)",
		"EXISTS (SELECT 1 FROM sqlite_master)",
		"EXISTS (SELECT 1 FROM sqlite_schema)",
		"(SELECT COUNT(*) FROM pragma_table_info('users')) > 0",
		"sqlite_version() != ''",
		"load_extension('x') IS NULL",
		"1=1; DELETE FROM subscribers",
		"1=1; ATTACH DATABASE '/tmp/x.db' AS x",
		"1=1) UNION SELECT * FROM users --",
		"subscribers.id IN (WITH u AS (SELECT id FROM users) SELECT id FROM u)",
		"randomblob(1000000000) IS NOT NULL",
	} {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Expr: expr}, store.Sort{}, store.Page{Limit: 10})
		var ie *store.InvalidQueryError
		if !errors.As(err, &ie) {
			t.Errorf("%q: got %v, want *store.InvalidQueryError", expr, err)
		}
		_, err = st.Subscribers().DeleteByFilter(ctx, store.SubscriberFilter{Expr: expr})
		if !errors.As(err, &ie) {
			t.Errorf("%q (delete by filter): got %v, want *store.InvalidQueryError", expr, err)
		}
	}

	// The denial comes from the authorizer, not from a syntax error.
	_, _, err = st.Subscribers().Query(ctx, store.SubscriberFilter{Expr: "(SELECT COUNT(*) FROM users) > 0"}, store.Sort{}, store.Page{Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("users table: want an authorizer denial, got %v", err)
	}

	// Ordinary expressions work, quoted semicolons included.
	for _, expr := range []string{
		"subscribers.attribs ->> 'city' = 'a;b'",
		"LOWER(subscribers.email) LIKE '%@example.com'",
		"subscribers.created_at > date('now', '-1 day')",
		"EXISTS (SELECT 1 FROM subscriber_lists sl WHERE sl.subscriber_id = subscribers.id)",
	} {
		if _, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Expr: expr}, store.Sort{}, store.Page{Limit: 10}); err != nil {
			t.Errorf("%q: %v", expr, err)
		}
	}
}
