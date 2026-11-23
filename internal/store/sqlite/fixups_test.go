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

// TestFixupListRole upgrades a database with upstream's users table
// (list_role_id ON DELETE CASCADE, as in tag v7.0.0-beta.2-sqlite.2) and
// checks that the rebuild keeps the rows and the ID counter, and runs once.
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
	if len(p) != 1 || !strings.Contains(p[0], "list_role_id") {
		t.Fatalf("pending = %v, want the list_role_id fixup", p)
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
