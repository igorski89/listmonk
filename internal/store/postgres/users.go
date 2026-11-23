package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type users struct{ *Store }
type roles struct{ *Store }

func (s users) Query(ctx context.Context, f store.UserFilter, srt store.Sort) ([]auth.User, error) {
	rows := []userRow{}
	stmt := replaceOrder(s.q.GetUsers, srt)
	if err := s.db.SelectContext(ctx, &rows, stmt, f.Type, f.Status, f.UserRoleID, f.ListRoleID); err != nil {
		return nil, wrap(err)
	}
	out := make([]auth.User, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out, nil
}

func (s users) Get(ctx context.Context, ref store.UserRef) (auth.User, error) {
	var row userRow
	if err := s.q.GetUser.GetContext(ctx, &row, ref.ID, ref.Username, ref.Email); err != nil {
		return auth.User{}, notFound(err)
	}
	return row.model(), nil
}

func (s users) Create(ctx context.Context, u auth.User) (int, error) {
	var id int
	if err := s.q.CreateUser.GetContext(ctx, &id, u.Username, u.PasswordLogin, u.Password, u.Email, u.Name, u.Type, u.UserRoleID, u.ListRoleID, u.Status); err != nil {
		return 0, wrap(err)
	}
	return id, nil
}

func (s users) Update(ctx context.Context, id int, u auth.User) error {
	// upstream's update-user takes -1 to remove the list role.
	listRoleID := -1
	if u.ListRoleID != nil {
		listRoleID = *u.ListRoleID
	}

	res, err := s.q.UpdateUser.ExecContext(ctx, id, u.Username, u.PasswordLogin, u.Password, u.Email, u.Name, u.Type, u.UserRoleID, listRoleID, u.Status)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// update-user updates nothing both when the user doesn't exist and
		// when the super admin guard stops it. Tell them apart.
		if _, err := s.Get(ctx, store.UserRef{ID: id}); errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return store.ErrLastSuperAdmin
	}
	return nil
}

func (s users) UpdateProfile(ctx context.Context, id int, u auth.User) error {
	res, err := s.q.UpdateUserProfile.ExecContext(ctx, id, u.Name, u.PasswordLogin, u.Password)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s users) UpdateEmail(ctx context.Context, id int, oldEmail, email string) error {
	res, err := s.q.UpdateUserEmail.ExecContext(ctx, id, oldEmail, email)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s users) Login(ctx context.Context, username, password string) (auth.User, error) {
	var row userRow
	if err := s.q.LoginUser.GetContext(ctx, &row, username, password); err != nil {
		return auth.User{}, notFound(err)
	}
	return row.model(), nil
}

func (s users) RecordLogin(ctx context.Context, id int, avatar string) error {
	_, err := s.q.UpdateUserLogin.ExecContext(ctx, id, avatar)
	return wrap(err)
}

func (s users) SetTwoFA(ctx context.Context, id int, typ, key string) error {
	_, err := s.q.SetUserTwoFA.ExecContext(ctx, id, typ, key)
	return wrap(err)
}

func (s users) Delete(ctx context.Context, ids []int) (int, error) {
	res, err := s.q.DeleteUsers.ExecContext(ctx, pq.Array(ids))
	if err != nil {
		return 0, wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return 0, store.ErrLastSuperAdmin
	}
	return int(n), nil
}

func (s users) DeleteSessions(ctx context.Context, userID int, exceptID string) error {
	_, err := s.q.DeleteUserSessions.ExecContext(ctx, strconv.Itoa(userID), exceptID)
	return wrap(err)
}

// Roles.

func (s roles) UserRoles(ctx context.Context, search string, srt store.Sort) ([]auth.Role, error) {
	rows := []roleRow{}
	stmt := replaceOrder(s.q.GetUserRoles, srt)
	if err := s.db.SelectContext(ctx, &rows, stmt, 0, search); err != nil {
		return nil, wrap(err)
	}
	out := make([]auth.Role, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out, nil
}

func (s roles) UserRole(ctx context.Context, id int) (auth.Role, error) {
	rows := []roleRow{}
	stmt := replaceOrder(s.q.GetUserRoles, store.Sort{Field: models.FieldCreatedAt})
	if err := s.db.SelectContext(ctx, &rows, stmt, id, ""); err != nil {
		return auth.Role{}, wrap(err)
	}
	if len(rows) == 0 {
		return auth.Role{}, store.ErrNotFound
	}
	return rows[0].model(), nil
}

func (s roles) ListRoles(ctx context.Context, search string, srt store.Sort) ([]auth.ListRole, error) {
	out := []auth.ListRole{}
	stmt := replaceOrder(s.q.GetListRoles, srt)
	if err := s.db.SelectContext(ctx, &out, stmt, search); err != nil {
		return nil, wrap(err)
	}

	// Unmarshall the nested list permissions, if any.
	for n, r := range out {
		if r.ListsRaw == nil {
			continue
		}
		if err := json.Unmarshal(r.ListsRaw, &out[n].Lists); err != nil {
			s.log.Printf("error unmarshalling list permissions for role %d: %v", r.ID, err)
		}
	}
	return out, nil
}

func (s roles) CreateUserRole(ctx context.Context, r auth.Role) (auth.Role, error) {
	var row roleRow
	if err := s.q.CreateRole.GetContext(ctx, &row, r.Name, auth.RoleTypeUser, pq.Array(r.Permissions)); err != nil {
		return auth.Role{}, wrap(err)
	}
	return row.model(), nil
}

func (s roles) CreateListRole(ctx context.Context, r auth.ListRole) (auth.ListRole, error) {
	var out auth.ListRole
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.StmtxContext(ctx, s.q.CreateRole).Unsafe().GetContext(ctx, &out, r.Name, auth.RoleTypeList, pq.Array([]string{})); err != nil {
			return err
		}
		return s.upsertListPermissions(ctx, tx, out.ID, r.Lists)
	})
	return out, wrap(err)
}

func (s roles) UpdateUserRole(ctx context.Context, id int, r auth.Role) (auth.Role, error) {
	var row roleRow
	if err := s.q.UpdateRole.GetContext(ctx, &row, id, r.Name, pq.Array(r.Permissions)); err != nil {
		return auth.Role{}, notFound(err)
	}
	if row.ID == 0 {
		return auth.Role{}, store.ErrNotFound
	}
	return row.model(), nil
}

func (s roles) UpdateListRole(ctx context.Context, id int, r auth.ListRole) (auth.ListRole, error) {
	var out auth.ListRole
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.StmtxContext(ctx, s.q.UpdateRole).Unsafe().GetContext(ctx, &out, id, r.Name, pq.Array([]string{})); err != nil {
			return err
		}
		if out.ID == 0 {
			return store.ErrNotFound
		}
		return s.upsertListPermissions(ctx, tx, out.ID, r.Lists)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrNotFound
	}
	return out, wrap(err)
}

// upsertListPermissions replaces a list role's per-list permissions.
func (s roles) upsertListPermissions(ctx context.Context, tx *sqlx.Tx, roleID int, lp []auth.ListPermission) error {
	var (
		listIDs   = make([]int, 0, len(lp))
		listPerms = make([][]string, 0, len(lp))
	)
	for _, p := range lp {
		if len(p.Permissions) == 0 {
			continue
		}
		listIDs = append(listIDs, p.ID)

		// For the Postgres array unnesting query to work, all permissions arrays should
		// have equal number of entries. Add "" in case there's only one of either list:get or list:manage
		perms := make([]string, 2)
		copy(perms[:], p.Permissions[:])
		listPerms = append(listPerms, perms)
	}
	_, err := tx.StmtxContext(ctx, s.q.UpsertListPermissions).ExecContext(ctx, roleID, pq.Array(listIDs), pq.Array(listPerms))
	return err
}

// roleInUse reports whether err is a role still referenced by users.
// (Upstream checked for "users_role_id_fkey", a constraint that doesn't
// exist in schema.sql, so the check never matched.)
func roleInUse(err error) bool {
	e := pqErr(err)
	if e == nil {
		return false
	}
	switch e.Constraint {
	case "users_role_id_fkey", "users_user_role_id_fkey", "users_list_role_id_fkey":
		return true
	}
	return false
}

func (s roles) DeleteListPermission(ctx context.Context, roleID, listID int) error {
	if _, err := s.q.DeleteListPermission.ExecContext(ctx, roleID, listID); err != nil {
		if roleInUse(err) {
			return store.ErrInUse
		}
		return wrap(err)
	}
	return nil
}

func (s roles) Delete(ctx context.Context, id int) error {
	if _, err := s.q.DeleteRole.ExecContext(ctx, id); err != nil {
		if roleInUse(err) {
			return store.ErrInUse
		}
		return wrap(err)
	}
	return nil
}

// inTx runs fn in a transaction, committing if it returns nil.
func (s *Store) inTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
