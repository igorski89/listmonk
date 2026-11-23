package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	pgsess "github.com/zerodha/simplesessions/stores/postgres/v3"
	"golang.org/x/crypto/bcrypt"
	null "gopkg.in/volatiletech/null.v6"
)

type users struct{ *Store }
type roles struct{ *Store }

// bcryptCost for new password hashes. Postgres' pgcrypto uses bf with 6
// rounds; both produce standard $2a$ hashes either backend can check.
const bcryptCost = 10

func hashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcryptCost)
	return string(b), err
}

type userRow struct {
	auth.User
	UserRolePerms jsonStrings    `db:"user_role_permissions"`
	ListsPermsRaw types.JSONText `db:"list_role_perms"`
}

func (r userRow) model() auth.User {
	u := r.User
	u.UserRolePerms = []string(r.UserRolePerms)
	if r.ListsPermsRaw != nil {
		raw := json.RawMessage(r.ListsPermsRaw)
		u.ListsPermsRaw = &raw
	}
	return u
}

// listRolePerms is a list role's per-list permissions as JSON.
const listRolePerms = `(SELECT json_group_array(json_object('id', cr.list_id, 'name', cl.name, 'permissions', json(cr.permissions)))
	FROM roles cr LEFT JOIN lists cl ON (cl.id = cr.list_id) WHERE cr.parent_id = lr.id AND cr.type = 'list')`

var userSortFields = map[string]string{
	"username":    "users.username",
	"email":       "users.email",
	"created_at":  "users.created_at",
	"loggedin_at": "users.loggedin_at",
}

func (s users) Query(ctx context.Context, f store.UserFilter, srt store.Sort) ([]auth.User, error) {
	rows := []userRow{}
	// get-users: a list role without per-list roles yields one entry
	// built from the role itself (upstream's LEFT JOIN).
	q := withOrder(`SELECT users.*, ur.name AS user_role_name, ur.permissions AS user_role_permissions,
		lr.name AS list_role_name,
		CASE WHEN lr.id IS NULL THEN NULL ELSE COALESCE(`+listRolePermsNonEmpty+`,
			json_array(json_object('id', lr.list_id, 'name', (SELECT name FROM lists WHERE id = lr.list_id), 'permissions', json(lr.permissions)))) END AS list_role_perms
		FROM users
		LEFT JOIN roles ur ON (users.user_role_id = ur.id AND ur.type = 'user' AND ur.parent_id IS NULL)
		LEFT JOIN roles lr ON (users.list_role_id = lr.id AND lr.type = 'list' AND lr.parent_id IS NULL)
		WHERE (?1 = '' OR users.type = ?1) AND (?2 = '' OR users.status = ?2)
		AND (?3 = 0 OR users.user_role_id = ?3) AND (?4 = 0 OR users.list_role_id = ?4)
		ORDER BY {ORDER}`, orderBy(srt, userSortFields, "users.created_at")+", users.id")
	if err := s.r.SelectContext(ctx, &rows, q, f.Type, f.Status, f.UserRoleID, f.ListRoleID); err != nil {
		return nil, wrap(err)
	}
	out := make([]auth.User, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out, nil
}

// listRolePermsNonEmpty is listRolePerms, NULL when the role has none.
const listRolePermsNonEmpty = `(SELECT CASE WHEN COUNT(*) = 0 THEN NULL ELSE json_group_array(json_object('id', cr.list_id, 'name', cl.name, 'permissions', json(cr.permissions))) END
	FROM roles cr LEFT JOIN lists cl ON (cl.id = cr.list_id) WHERE cr.parent_id = lr.id AND cr.type = 'list')`

func (s users) Get(ctx context.Context, ref store.UserRef) (auth.User, error) {
	var row userRow
	if err := s.r.GetContext(ctx, &row, `SELECT users.*, ur.id AS user_role_id, ur.name AS user_role_name, ur.permissions AS user_role_permissions,
		lr.id AS list_role_id, lr.name AS list_role_name, `+listRolePermsNonEmpty+` AS list_role_perms
		FROM users
		LEFT JOIN roles ur ON (users.user_role_id = ur.id AND ur.type = 'user' AND ur.parent_id IS NULL)
		LEFT JOIN roles lr ON (users.list_role_id = lr.id AND lr.type = 'list' AND lr.parent_id IS NULL)
		WHERE CASE WHEN ?1 != 0 THEN users.id = ?1 WHEN ?2 != '' THEN users.username = ?2 WHEN ?3 != '' THEN users.email = ?3 END`,
		ref.ID, ref.Username, ref.Email); err != nil {
		return auth.User{}, notFound(err)
	}
	return row.model(), nil
}

func (s users) Create(ctx context.Context, u auth.User) (int, error) {
	// API tokens are stored as given; passwords are hashed; no password
	// otherwise.
	var password any
	switch {
	case u.Type == auth.UserTypeAPI:
		password = u.Password
	case u.PasswordLogin && u.Password.String != "":
		h, err := hashPassword(u.Password.String)
		if err != nil {
			return 0, err
		}
		password = h
	}
	var listRole any
	if u.ListRoleID != nil {
		listRole = *u.ListRoleID
	}
	t := now()
	var id int
	if err := s.w.GetContext(ctx, &id, `INSERT INTO users (username, password_login, password, email, name, type, user_role_id, list_role_id, status, created_at, updated_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, (SELECT id FROM roles WHERE id = ?7 AND type = 'user'), (SELECT id FROM roles WHERE id = ?8 AND type = 'list'), ?9, ?10, ?10)
		RETURNING id`, u.Username, u.PasswordLogin, password, u.Email, u.Name, u.Type, u.UserRoleID, listRole, u.Status, t); err != nil {
		return 0, wrap(err)
	}
	return id, nil
}

func (s users) Update(ctx context.Context, id int, u auth.User) error {
	listRoleID := -1
	if u.ListRoleID != nil {
		listRoleID = *u.ListRoleID
	}
	var hash any
	if u.PasswordLogin && u.Password.String != "" {
		h, err := hashPassword(u.Password.String)
		if err != nil {
			return err
		}
		hash = h
	}

	var n int64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		// The last enabled super admin can't be disabled or demoted.
		var others int
		if err := tx.GetContext(ctx, &others, `SELECT COUNT(*) FROM users WHERE id != ?1 AND status = 'enabled' AND type = 'user' AND user_role_id = 1`, id); err != nil {
			return wrap(err)
		}
		if others == 0 && (u.UserRoleID != 1 || u.Status != auth.UserStatusEnabled) {
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE users SET
			username = (CASE WHEN ?2 != '' THEN ?2 ELSE username END),
			password_login = ?3,
			password = (CASE
				WHEN users.type = 'api' AND (?6 = '' OR ?6 = 'api') THEN password
				WHEN ?3 THEN COALESCE(?4, password)
				ELSE NULL END),
			email = (CASE WHEN ?5 != '' THEN ?5 ELSE email END),
			name = (CASE WHEN ?7 != '' THEN ?7 ELSE name END),
			type = (CASE WHEN ?6 != '' THEN ?6 ELSE type END),
			user_role_id = (CASE WHEN ?8 != 0 THEN (SELECT id FROM roles WHERE id = ?8 AND type = 'user') ELSE user_role_id END),
			list_role_id = (CASE WHEN ?9 < 0 THEN NULL WHEN ?9 > 0 THEN (SELECT id FROM roles WHERE id = ?9 AND type = 'list') ELSE list_role_id END),
			status = (CASE WHEN ?10 != '' THEN ?10 ELSE status END),
			updated_at = ?11
			WHERE id = ?1`, id, u.Username, u.PasswordLogin, hash, u.Email, u.Type, u.Name, u.UserRoleID, listRoleID, u.Status, now())
		if err != nil {
			return wrap(err)
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.Get(ctx, store.UserRef{ID: id}); errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return store.ErrLastSuperAdmin
	}
	return nil
}

func (s users) UpdateProfile(ctx context.Context, id int, u auth.User) error {
	var hash any
	if u.PasswordLogin && u.Password.String != "" {
		h, err := hashPassword(u.Password.String)
		if err != nil {
			return err
		}
		hash = h
	}
	res, err := s.w.ExecContext(ctx, `UPDATE users SET name = ?2,
		password = (CASE WHEN ?3 THEN COALESCE(?4, password) ELSE NULL END)
		WHERE id = ?1`, id, u.Name, u.PasswordLogin, hash)
	if err != nil {
		return wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s users) UpdateEmail(ctx context.Context, id int, oldEmail, email string) error {
	res, err := s.w.ExecContext(ctx, `UPDATE users SET email = ?3, updated_at = ?4
		WHERE id = ?1 AND email = ?2 AND status = 'enabled' AND password_login = true AND type = 'user'`,
		id, oldEmail, email, now())
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
	if err := s.r.GetContext(ctx, &row, `SELECT * FROM users WHERE username = ? AND status != 'disabled' AND password_login = true`, username); err != nil {
		return auth.User{}, notFound(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(row.Password.String), []byte(password)) != nil {
		return auth.User{}, store.ErrNotFound
	}
	if err := s.w.GetContext(ctx, &row, `UPDATE users SET loggedin_at = ?2 WHERE id = ?1 RETURNING *`, row.ID, now()); err != nil {
		return auth.User{}, wrap(err)
	}
	return row.model(), nil
}

func (s users) RecordLogin(ctx context.Context, id int, avatar string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE users SET loggedin_at = ?2, avatar = NULLIF(?3, '') WHERE id = ?1`, id, now(), avatar)
	return wrap(err)
}

func (s users) SetTwoFA(ctx context.Context, id int, typ, key string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE users SET twofa_type = ?2, twofa_key = ?3, updated_at = ?4 WHERE id = ?1`, id, typ, key, now())
	return wrap(err)
}

func (s users) Delete(ctx context.Context, ids []int) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM users WHERE id IN (SELECT value FROM json_each(?1))
		AND (SELECT COUNT(*) FROM users WHERE id NOT IN (SELECT value FROM json_each(?1)) AND user_role_id = 1 AND type = 'user' AND status = 'enabled') > 0`,
		ints(ids))
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
	_, err := s.w.ExecContext(ctx, `DELETE FROM sessions WHERE CAST(data ->> '$.user_id' AS TEXT) = ?1 AND (?2 = '' OR id != ?2)`,
		strconv.Itoa(userID), exceptID)
	return wrap(err)
}

// Roles.

type roleRow struct {
	auth.Role
	Permissions jsonStrings    `db:"permissions"`
	ListsRaw    types.JSONText `db:"list_permissions"`
}

func (r roleRow) model() auth.Role {
	o := r.Role
	o.Permissions = []string(r.Permissions)
	o.ListsRaw = json.RawMessage(r.ListsRaw)
	return o
}

type listRoleRow struct {
	auth.ListRole
	ListsRaw types.JSONText `db:"list_permissions"`
}

var roleSortFields = map[string]string{
	"name":       "p.name",
	"created_at": "p.created_at",
	"updated_at": "p.updated_at",
}

// rolesSQL lists main roles of a type with their per-list permissions.
const rolesSQL = `SELECT p.*, CAST(COALESCE((SELECT json_group_array(json_object('id', c.list_id, 'name', l.name, 'permissions', json(c.permissions)))
	FROM roles c LEFT JOIN lists l ON (l.id = c.list_id) WHERE c.parent_id = p.id), '[]') AS BLOB) AS list_permissions
	FROM roles p WHERE p.type = ?1 AND p.parent_id IS NULL AND (?2 = 0 OR p.id = ?2)
	AND (?3 = '' OR p.name LIKE '%' || ?3 || '%')
	ORDER BY {ORDER}`

func (s roles) UserRoles(ctx context.Context, search string, srt store.Sort) ([]auth.Role, error) {
	rows := []roleRow{}
	if err := s.r.SelectContext(ctx, &rows, withOrder(rolesSQL, orderBy(srt, roleSortFields, "p.created_at")+", p.id"), auth.RoleTypeUser, 0, search); err != nil {
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
	if err := s.r.SelectContext(ctx, &rows, withOrder(rolesSQL, "p.created_at"), auth.RoleTypeUser, id, ""); err != nil {
		return auth.Role{}, wrap(err)
	}
	if len(rows) == 0 {
		return auth.Role{}, store.ErrNotFound
	}
	return rows[0].model(), nil
}

func (s roles) ListRoles(ctx context.Context, search string, srt store.Sort) ([]auth.ListRole, error) {
	rows := []listRoleRow{}
	if err := s.r.SelectContext(ctx, &rows, withOrder(rolesSQL, orderBy(srt, roleSortFields, "p.created_at")+", p.id"), auth.RoleTypeList, 0, search); err != nil {
		return nil, wrap(err)
	}
	out := make([]auth.ListRole, len(rows))
	for i, r := range rows {
		out[i] = r.ListRole
		out[i].ListsRaw = json.RawMessage(r.ListsRaw)
		if err := json.Unmarshal(r.ListsRaw, &out[i].Lists); err != nil {
			s.log.Printf("error unmarshalling list permissions for role %d: %v", r.ID, err)
		}
	}
	return out, nil
}

func (s roles) CreateUserRole(ctx context.Context, r auth.Role) (auth.Role, error) {
	var row roleRow
	t := now()
	if err := s.w.GetContext(ctx, &row, `INSERT INTO roles (name, type, permissions, created_at, updated_at) VALUES (?, ?, ?, ?, ?) RETURNING *, '[]' AS list_permissions`,
		r.Name, auth.RoleTypeUser, strs(r.Permissions), t, t); err != nil {
		return auth.Role{}, wrap(err)
	}
	return row.model(), nil
}

// upsertListPermissions replaces a list role's per-list roles.
func upsertListPermissions(ctx context.Context, tx *sqlx.Tx, roleID int, lp []auth.ListPermission) error {
	ids := make([]int, 0, len(lp))
	for _, p := range lp {
		if len(p.Permissions) > 0 {
			ids = append(ids, p.ID)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE parent_id = ?1 AND list_id NOT IN (SELECT value FROM json_each(?2))`, roleID, ints(ids)); err != nil {
		return wrap(err)
	}
	t := now()
	for _, p := range lp {
		perms := make([]string, 0, len(p.Permissions))
		for _, x := range p.Permissions {
			if x != "" {
				perms = append(perms, x)
			}
		}
		if len(p.Permissions) == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO roles (parent_id, list_id, permissions, type, created_at, updated_at)
			VALUES (?1, ?2, ?3, 'list', ?4, ?4) ON CONFLICT (parent_id, list_id) DO UPDATE SET permissions = excluded.permissions`,
			roleID, p.ID, strs(perms), t); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func (s roles) CreateListRole(ctx context.Context, r auth.ListRole) (auth.ListRole, error) {
	var out auth.ListRole
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		t := now()
		if err := tx.GetContext(ctx, &out, `INSERT INTO roles (name, type, permissions, created_at, updated_at) VALUES (?, 'list', '[]', ?, ?)
			RETURNING id, name, list_id, parent_id, created_at, updated_at`, r.Name, t, t); err != nil {
			return wrap(err)
		}
		return upsertListPermissions(ctx, tx, out.ID, r.Lists)
	})
	return out, err
}

func (s roles) UpdateUserRole(ctx context.Context, id int, r auth.Role) (auth.Role, error) {
	var row roleRow
	if err := s.w.GetContext(ctx, &row, `UPDATE roles SET name = ?2, permissions = ?3, updated_at = ?4 WHERE id = ?1 AND parent_id IS NULL
		RETURNING *, '[]' AS list_permissions`, id, r.Name, strs(r.Permissions), now()); err != nil {
		return auth.Role{}, notFound(err)
	}
	return row.model(), nil
}

func (s roles) UpdateListRole(ctx context.Context, id int, r auth.ListRole) (auth.ListRole, error) {
	var out auth.ListRole
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out, `UPDATE roles SET name = ?2, permissions = '[]', updated_at = ?3 WHERE id = ?1 AND parent_id IS NULL
			RETURNING id, name, list_id, parent_id, created_at, updated_at`, id, r.Name, now()); err != nil {
			return notFound(err)
		}
		return upsertListPermissions(ctx, tx, out.ID, r.Lists)
	})
	return out, err
}

func (s roles) DeleteListPermission(ctx context.Context, roleID, listID int) error {
	if _, err := s.w.ExecContext(ctx, `DELETE FROM roles WHERE parent_id = ? AND list_id = ?`, roleID, listID); err != nil {
		if isFKErr(err) {
			return store.ErrInUse
		}
		return wrap(err)
	}
	return nil
}

func (s roles) Delete(ctx context.Context, id int) error {
	if _, err := s.w.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id); err != nil {
		if isFKErr(err) {
			return store.ErrInUse
		}
		return wrap(err)
	}
	return nil
}

// Sessions: the simplesessions store for SQLite, with the same semantics
// and errors as its Postgres store (data is a JSON object; values come
// back as JSON types, numbers as float64).

type sessions struct{ *Store }

func (s sessions) ttl() time.Duration {
	if s.opt.SessionTTL < time.Second {
		return 24 * time.Hour
	}
	return s.opt.SessionTTL
}

func (s sessions) Create(id string) error {
	_, err := s.w.Exec(`INSERT INTO sessions (id, data, created_at) VALUES (?, '{}', ?)`, id, now())
	return err
}

func (s sessions) GetAll(id string) (map[string]any, error) {
	var b []byte
	if err := s.r.Get(&b, `SELECT CAST(data AS BLOB) FROM sessions WHERE id = ? AND created_at >= ?`, id, ts(time.Now().Add(-s.ttl()))); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, pgsess.ErrInvalidSession
		}
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s sessions) Get(id, key string) (any, error) {
	vals, err := s.GetAll(id)
	if err != nil {
		return nil, err
	}
	return vals[key], nil
}

func (s sessions) GetMulti(id string, keys ...string) (map[string]any, error) {
	vals, err := s.GetAll(id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = vals[k]
	}
	return out, nil
}

func (s sessions) Set(id, key string, val any) error {
	return s.SetMulti(id, map[string]any{key: val})
}

func (s sessions) SetMulti(id string, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.affected(s.w.Exec(`UPDATE sessions SET data = json_patch(data, ?2) WHERE id = ?1`, id, string(b)))
}

func (s sessions) Delete(id string, keys ...string) error {
	// As the Postgres store's "data #- keys": the keys form one path.
	path := "$"
	for _, k := range keys {
		path += `."` + strings.ReplaceAll(k, `"`, `\"`) + `"`
	}
	return s.affected(s.w.Exec(`UPDATE sessions SET data = json_remove(data, ?2) WHERE id = ?1`, id, path))
}

func (s sessions) Clear(id string) error {
	return s.affected(s.w.Exec(`UPDATE sessions SET data = '{}' WHERE id = ?`, id))
}

func (s sessions) Destroy(id string) error {
	return s.affected(s.w.Exec(`DELETE FROM sessions WHERE id = ?`, id))
}

func (s sessions) affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return pgsess.ErrInvalidSession
	}
	return nil
}

func (s sessions) Prune() error {
	_, err := s.w.Exec(`DELETE FROM sessions WHERE created_at <= ?`, ts(time.Now().Add(-s.ttl())))
	return err
}

// Type helpers, identical to the Postgres store's.
var pgHelpers = &pgsess.Store{}

func (s sessions) Int(r any, err error) (int, error)         { return pgHelpers.Int(r, err) }
func (s sessions) Int64(r any, err error) (int64, error)     { return pgHelpers.Int64(r, err) }
func (s sessions) UInt64(r any, err error) (uint64, error)   { return pgHelpers.UInt64(r, err) }
func (s sessions) Float64(r any, err error) (float64, error) { return pgHelpers.Float64(r, err) }
func (s sessions) String(r any, err error) (string, error)   { return pgHelpers.String(r, err) }
func (s sessions) Bytes(r any, err error) ([]byte, error)    { return pgHelpers.Bytes(r, err) }
func (s sessions) Bool(r any, err error) (bool, error)       { return pgHelpers.Bool(r, err) }

var _ = models.OrderAsc
var _ = null.String{}
