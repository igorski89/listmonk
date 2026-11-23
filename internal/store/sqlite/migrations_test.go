package sqlite_test

import (
	"context"
	"database/sql"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/sqlite"
	null "gopkg.in/volatiletech/null.v6"
)

// TestFixupListRole upgrades a database as tag v7.0.0-beta.2-sqlite.2 left
// it (list_role_id ON DELETE CASCADE, no ledger) and checks that the
// rebuild keeps the rows and the ID counter, and runs once.
func TestFixupListRole(t *testing.T) {
	ctx := context.Background()
	lo := log.New(io.Discard, "", 0)
	env := store.MigrationEnv{Log: lo}
	path := filepath.Join(t.TempDir(), "listmonk.db")

	open := func() *sqlite.Store {
		t.Helper()
		st, err := sqlite.Open(path, lo)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Configure(store.Options{}); err != nil {
			t.Fatal(err)
		}
		return st
	}

	// Users with a list role, and a deleted top ID (the counter is ahead).
	st, err := sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Schema().Install(ctx, "v7.0.0", env); err != nil {
		t.Fatal(err)
	}
	if err := st.Configure(store.Options{}); err != nil {
		t.Fatal(err)
	}
	ur, err := st.Roles().CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Editors"), Permissions: []string{"lists:get_all"}})
	if err != nil {
		t.Fatal(err)
	}
	lr, err := st.Roles().CreateListRole(ctx, auth.ListRole{Name: null.StringFrom("Newsroom")})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, n := range []string{"editor", "writer", "gone"} {
		id, err := st.Users().Create(ctx, auth.User{Username: n, PasswordLogin: true, Password: null.StringFrom(n + "pass1234"),
			Email: null.StringFrom(n + "@example.com"), Name: n, Type: "user", UserRoleID: ur.ID, ListRoleID: &lr.ID, Status: "enabled"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := st.Users().Delete(ctx, ids[2:]); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Back to upstream's definition.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(ddl, "ON DELETE SET NULL", "ON DELETE CASCADE", 1)
	old = strings.Replace(old, "CREATE TABLE users", "CREATE TABLE users_old", 1)
	if old == ddl {
		t.Fatal("users DDL has no ON DELETE SET NULL")
	}
	for _, q := range []string{
		`PRAGMA foreign_keys = OFF`,
		`BEGIN`,
		old,
		`INSERT INTO users_old SELECT * FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_old RENAME TO users`,
		`UPDATE sqlite_sequence SET seq = ? WHERE name = 'users'`,
		`DROP TABLE schema_migrations`,
		`COMMIT`,
	} {
		var args []any
		if strings.HasPrefix(q, "UPDATE") {
			args = []any{ids[2]}
		}
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	onDelete := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT on_delete FROM pragma_foreign_key_list('users') WHERE "from" = 'list_role_id'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	seq := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'users'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := onDelete(); got != "CASCADE" {
		t.Fatalf("setup: on_delete = %s", got)
	}

	// The fixup is pending, runs, and then isn't.
	st = open()
	defer func() { st.Close() }()
	p, err := st.Schema().Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || p[0] != "fork-users-list-role-set-null" {
		t.Fatalf("pending = %v, want [fork-users-list-role-set-null]", p)
	}
	for i := 0; i < 2; i++ {
		if err := st.Schema().Migrate(ctx, env, false); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
	if p, _ := st.Schema().Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after migrate = %v", p)
	}

	if got := onDelete(); got != "SET NULL" {
		t.Errorf("on_delete = %s, want SET NULL", got)
	}
	if got := seq(); got != ids[2] {
		t.Errorf("users sequence = %d, want %d", got, ids[2])
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Errorf("integrity_check: %s %v", ic, err)
	}

	// Rows kept; deleting the list role keeps its users; IDs not reused.
	us, err := st.Users().Query(ctx, store.UserFilter{}, store.Sort{})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("users after rebuild = %d, want 2", len(us))
	}
	if err := st.Roles().Delete(ctx, lr.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		u, err := st.Users().Get(ctx, store.UserRef{ID: id})
		if err != nil {
			t.Fatalf("user %d after deleting its list role: %v", id, err)
		}
		if u.ListRoleID != nil {
			t.Errorf("user %d: list role still %d", id, *u.ListRoleID)
		}
	}
	id, err := st.Users().Create(ctx, auth.User{Username: "new", PasswordLogin: true, Password: null.StringFrom("newpass1234"),
		Email: null.StringFrom("new@example.com"), Name: "new", Type: "user", UserRoleID: ur.ID, Status: "enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if id != ids[2]+1 {
		t.Errorf("new user ID = %d, want %d (deleted ID reused?)", id, ids[2]+1)
	}
}

// TestStepsLedger: a fresh install has every step recorded; a database
// from before the ledger whose schema already has a step's change (tag
// v7.0.0-beta.2-sqlite.3: the list role fixup ran) records it without
// changing anything.
func TestStepsLedger(t *testing.T) {
	ctx := context.Background()
	lo := log.New(io.Discard, "", 0)
	env := store.MigrationEnv{Log: lo}
	path := filepath.Join(t.TempDir(), "listmonk.db")
	st, err := sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	if p, err := st.Schema().Pending(ctx); err != nil || len(p) != 0 {
		t.Fatalf("pending before install = %v %v, want none", p, err)
	}
	if err := st.Schema().Install(ctx, "v7.0.0", env); err != nil {
		t.Fatal(err)
	}
	if p, err := st.Schema().Pending(ctx); err != nil || len(p) != 0 {
		t.Fatalf("pending after install = %v %v, want none", p, err)
	}
	st.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	var before string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&before)

	st, err = sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Schema().Pending(ctx)
	if err != nil || len(p) == 0 {
		t.Fatalf("pending without a ledger = %v %v, want every step", p, err)
	}
	if err := st.Schema().Migrate(ctx, env, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Schema().Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after migrate = %v", p)
	}
	var after string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&after)
	if after != before {
		t.Errorf("users rebuilt although already SET NULL:\n%s\n%s", before, after)
	}
}

// TestFixupListRoleColumnGuard: the users rebuild has a fixed definition,
// so it refuses to run on a users table with other columns.
func TestFixupListRoleColumnGuard(t *testing.T) {
	ctx := context.Background()
	lo := log.New(io.Discard, "", 0)
	env := store.MigrationEnv{Log: lo}
	path := filepath.Join(t.TempDir(), "listmonk.db")
	st, err := sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Schema().Install(ctx, "v7.0.0", env); err != nil {
		t.Fatal(err)
	}
	st.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(ddl, "ON DELETE SET NULL", "ON DELETE CASCADE", 1)
	old = strings.Replace(old, "CREATE TABLE users", "CREATE TABLE users_old", 1)
	for _, q := range []string{`BEGIN`, old, `DROP TABLE users`, `ALTER TABLE users_old RENAME TO users`,
		`ALTER TABLE users ADD COLUMN extra TEXT`, `DROP TABLE schema_migrations`, `COMMIT`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	st, err = sqlite.Open(path, lo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.Schema().Migrate(ctx, env, false)
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("migrate = %v, want a column mismatch naming extra", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name = 'extra'`).Scan(&n)
	if n != 1 {
		t.Errorf("column extra lost")
	}
}
