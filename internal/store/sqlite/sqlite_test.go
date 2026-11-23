package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/sqlite"
	"github.com/knadh/listmonk/internal/store/storetest"
	"github.com/knadh/listmonk/models"
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

// TestSchemaDrift compares the SQLite schema with upstream's schema.sql:
// the same tables, columns and default settings. (Scanning with Unsafe()
// would hide a missing column.)
func TestSchemaDrift(t *testing.T) {
	pg, err := os.ReadFile("../../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	// Postgres: table -> columns, from the CREATE TABLE statements.
	pgCols := map[string]map[string]bool{}
	reTable := regexp.MustCompile(`(?s)CREATE TABLE (\w+) \((.*?)\n\);`)
	reCol := regexp.MustCompile(`^([a-z_]+)\s`)
	for _, m := range reTable.FindAllStringSubmatch(string(pg), -1) {
		cols := map[string]bool{}
		for _, line := range strings.Split(m[2], "\n") {
			line = strings.TrimSpace(line)
			if c := reCol.FindStringSubmatch(line); c != nil {
				cols[c[1]] = true
			}
		}
		pgCols[m[1]] = cols
	}
	if len(pgCols) < 10 {
		t.Fatalf("parsed only %d tables from schema.sql", len(pgCols))
	}

	lo := log.New(io.Discard, "", 0)
	path := filepath.Join(t.TempDir(), "drift.db")
	st, err := sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Schema().Install(ctx, "v7.0.0", store.MigrationEnv{Log: lo}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var liteTables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		liteTables = append(liteTables, n)
	}
	rows.Close()
	for _, tb := range liteTables {
		if pgCols[tb] == nil && tb != "schema_migrations" { // the fork's SQLite step ledger
			t.Errorf("table %s only in SQLite", tb)
		}
	}

	for table, cols := range pgCols {
		lite := map[string]bool{}
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var n string
			rows.Scan(&n)
			lite[n] = true
		}
		rows.Close()
		if len(lite) == 0 {
			t.Errorf("table %s missing in SQLite", table)
			continue
		}
		for c := range cols {
			if !lite[c] {
				t.Errorf("table %s: column %s missing in SQLite", table, c)
			}
		}
		for c := range lite {
			if !cols[c] {
				t.Errorf("table %s: column %s only in SQLite", table, c)
			}
		}
	}

	// Default settings keys.
	reKey := regexp.MustCompile(`\('([a-z0-9_.]+)',`)
	i := strings.Index(string(pg), "INSERT INTO settings")
	j := strings.Index(string(pg)[i:], ";\n")
	want := map[string]bool{}
	for _, m := range reKey.FindAllStringSubmatch(string(pg)[i:i+j], -1) {
		want[m[1]] = true
	}
	raw, err := st.Settings().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("setting %s missing in SQLite", k)
		}
	}
	for k := range got {
		if !want[k] && k != "migrations" {
			t.Errorf("setting %s only in SQLite", k)
		}
	}
}

// TestConcurrent runs an import, view tracking, subscriber queries and the
// campaign loop at the same time: with one writer connection they must
// queue, not fail with SQLITE_BUSY or deadlock.
func TestConcurrent(t *testing.T) {
	lo := log.New(io.Discard, "", 0)
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "conc.db"), lo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Schema().Install(ctx, "v7.0.0", store.MigrationEnv{Log: lo}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configure(store.Options{SessionTTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	l, err := st.Lists().Create(ctx, models.List{Name: "l", Type: "private", Optin: "single", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Templates().Create(ctx, store.TemplateInput{Name: "T", Type: "campaign", Body: `{{ template "content" . }}`}); err != nil {
		t.Fatal(err)
	}
	camp, err := st.Campaigns().Create(ctx, models.Campaign{Type: "regular", Name: "c", Subject: "s", FromEmail: "a@b.c", Body: "b",
		ContentType: "richtext", Messenger: "email", Headers: models.Headers{}, Attribs: models.JSON{}}, []int{l.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	run := func(name string, fn func(i int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := fn(i); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
					return
				}
			}
		}()
	}
	run("import", func(i int) error {
		rows := make([]store.SubscriberInput, 20)
		for j := range rows {
			rows[j] = store.SubscriberInput{Email: fmt.Sprintf("s%d-%d@example.com", i, j), Name: "S"}
		}
		return st.Subscribers().Import(ctx, rows, store.ImportOptions{Mode: store.ImportSubscribe, ListIDs: []int{l.ID}, SubscriptionStatus: "confirmed", Permitted: store.ListScope{All: true}})
	})
	run("views", func(i int) error { return st.Tracking().RegisterView(ctx, camp.UUID, "") })
	run("query", func(i int) error {
		_, _, err := st.Subscribers().Query(ctx, store.SubscriberFilter{Expr: "subscribers.name = 'S'"}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	run("lists", func(i int) error {
		_, _, err := st.Lists().Query(ctx, store.ListFilter{Scope: store.ListScope{All: true}}, store.Sort{}, store.Page{Limit: 20})
		return err
	})
	run("next", func(i int) error {
		if i == 0 {
			if err := st.Campaigns().SetStatus(ctx, camp.ID, "running"); err != nil {
				return err
			}
		}
		_, err := st.Campaigns().Next(ctx, nil)
		return err
	})
	run("sessions", func(i int) error {
		id := fmt.Sprintf("sess-%d", i)
		if err := st.Sessions().Create(id); err != nil {
			return err
		}
		return st.Sessions().Set(id, "user_id", i)
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	_, total, err := st.Subscribers().Query(ctx, store.SubscriberFilter{}, store.Sort{}, store.Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 50*20 {
		t.Errorf("imported %d subscribers, want %d", total, 50*20)
	}
}
