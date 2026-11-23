package postgres_test

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	null "gopkg.in/volatiletech/null.v6"
)

// TestFixupListRole upgrades a database with upstream's foreign key on
// users.list_role_id (ON DELETE CASCADE), under a name other than
// upstream's, so the fixup has to find it in the catalog.
func TestFixupListRole(t *testing.T) {
	ctx := context.Background()
	st, db := openPG(t, store.Options{})
	env := store.MigrationEnv{Log: log.New(io.Discard, "", 0)}

	ur, err := st.Roles().CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Editors"), Permissions: []string{"lists:get_all"}})
	if err != nil {
		t.Fatal(err)
	}
	lr, err := st.Roles().CreateListRole(ctx, auth.ListRole{Name: null.StringFrom("Newsroom")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Users().Create(ctx, auth.User{Username: "editor", PasswordLogin: true, Password: null.StringFrom("editorpass1234"),
		Email: null.StringFrom("editor@example.com"), Name: "Ed", Type: "user", UserRoleID: ur.ID, ListRoleID: &lr.ID, Status: "enabled"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`ALTER TABLE users DROP CONSTRAINT users_list_role_id_fkey;
		ALTER TABLE users ADD CONSTRAINT "old list role fk" FOREIGN KEY (list_role_id) REFERENCES roles(id) ON DELETE CASCADE`); err != nil {
		t.Fatal(err)
	}
	fks := func() []string {
		t.Helper()
		var out []string
		if err := db.Select(&out, `SELECT c.conname || ':' || c.confdeltype::text FROM pg_constraint c
			JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
			WHERE c.conrelid = 'users'::regclass AND c.contype = 'f' AND a.attname = 'list_role_id'`); err != nil {
			t.Fatal(err)
		}
		return out
	}

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
	if got := fks(); len(got) != 1 || got[0] != "users_list_role_id_fkey:n" {
		t.Errorf("list_role_id foreign keys = %v, want [users_list_role_id_fkey:n]", got)
	}

	if err := st.Roles().Delete(ctx, lr.ID); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().Get(ctx, store.UserRef{ID: id})
	if err != nil {
		t.Fatalf("user after deleting its list role: %v", err)
	}
	if u.ListRoleID != nil {
		t.Errorf("list role still %d", *u.ListRoleID)
	}
}
