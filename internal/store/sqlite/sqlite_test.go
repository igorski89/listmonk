package sqlite_test

import (
	"context"
	"io"
	"log"
	"path/filepath"
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
