package postgres_test

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/goyesql/v2"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/postgres"
	"github.com/knadh/listmonk/internal/store/storetest"
	"github.com/knadh/stuffbin"
	_ "github.com/lib/pq"
)

// TestStore runs the conformance suite on Postgres. It needs a database it
// may wipe: LISTMONK_TEST_PG_DSN, e.g.
// "host=127.0.0.1 port=55432 user=listmonk password=listmonk dbname=listmonk sslmode=disable"
// (tests/parity/setup.sh starts one).
func TestStore(t *testing.T) {
	open := func(t *testing.T, o store.Options) store.Store {
		t.Helper()
		st, _ := openPG(t, o)
		return st
	}

	storetest.Run(t, open, storetest.Fixtures{
		CityBerlin:     "subscribers.attribs->>'city' = 'Berlin'",
		ForbiddenTable: "(SELECT COUNT(*) FROM users) > 0",
		Write:          "1=1; DELETE FROM subscribers",
	})
}

// openPG wipes the LISTMONK_TEST_PG_DSN database (skipping the test if it
// isn't set, failing it under CI), installs the schema and returns the store
// and its pool.
func openPG(t *testing.T, o store.Options) (*postgres.Store, *sqlx.DB) {
	t.Helper()
	dsn := os.Getenv("LISTMONK_TEST_PG_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("LISTMONK_TEST_PG_DSN not set under CI")
		}
		t.Skip("LISTMONK_TEST_PG_DSN not set")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := stuffbin.NewLocalFS("/", filepath.Join(root, "schema.sql")+":/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	lo := log.New(io.Discard, "", 0)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db = db.Unsafe()
	if _, err := db.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		t.Fatal(err)
	}

	qMap := goyesql.Queries{}
	files, _ := filepath.Glob(filepath.Join(root, "queries", "*.sql"))
	for _, f := range files {
		q, err := goyesql.ParseFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range q {
			qMap[k] = v
		}
	}

	st := postgres.New(db, qMap, nil, lo)
	if err := st.Schema().Install(context.Background(), "v7.0.0", store.MigrationEnv{FS: fs, Log: lo}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configure(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, db
}
