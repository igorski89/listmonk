package store

import (
	"context"
	"errors"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/zerodha/simplesessions/v3"
)

// ErrLastSuperAdmin: the change would leave no enabled user with the
// Super Admin role (deleting, disabling or demoting the last one).
var ErrLastSuperAdmin = errors.New("store: would remove the last super admin")

// UserRef addresses one user by ID (if != 0), else username, else e-mail.
type UserRef struct {
	ID       int
	Username string
	Email    string
}

// UserFilter selects users. Zero values mean "no condition".
type UserFilter struct {
	Type       string // "user" / "api"
	Status     string
	UserRoleID int
	ListRoleID int
}

// UserStore is admin users and API users.
//
// Passwords cross this interface in plain text and are hashed (bcrypt)
// by the store, never stored or returned in plain text: Postgres keeps
// upstream's pgcrypto CRYPT() SQL, SQLite uses golang.org/x/crypto/bcrypt.
// Both produce and accept the same $2a$ hashes, so a database moved
// between backends keeps working logins. API tokens are stored as given
// (the caller passes auth.HashAPIToken's output).
type UserStore interface {
	// Query returns users with their roles. Sort.Field is one of
	// username, email, created_at, loggedin_at.
	Query(ctx context.Context, f UserFilter, s Sort) ([]auth.User, error)

	// Get returns one user with roles, or ErrNotFound.
	Get(ctx context.Context, ref UserRef) (auth.User, error)

	// Create inserts a user and returns its ID. u.Password is the plain
	// password (password login) or the hashed API token (API users).
	// Role IDs that don't exist or have the wrong type are stored as
	// none. *ConflictError{Field: "username" | "email"} on duplicates.
	Create(ctx context.Context, u auth.User) (int, error)

	// Update changes a user. Empty fields keep their value; an empty
	// password keeps the current one; PasswordLogin=false clears the
	// password (API users keep their token); ListRoleID nil removes the
	// list role. ErrLastSuperAdmin if it would disable or demote the last
	// super admin, ErrNotFound if the user doesn't exist. (Upstream's
	// update-user returns 0 rows for both; the Postgres store tells them
	// apart with a lookup on that failure path only.)
	Update(ctx context.Context, id int, u auth.User) error

	// UpdateProfile is a user editing themselves; only u's Name, Email
	// (applied only to password-login users), PasswordLogin and Password
	// (empty = keep; PasswordLogin=false clears it) are used. ErrNotFound
	// if missing.
	UpdateProfile(ctx context.Context, id int, u auth.User) error

	// Login returns the enabled, password-login user whose password
	// matches and records the login time; ErrNotFound otherwise (unknown
	// user and wrong password are not distinguished).
	Login(ctx context.Context, username, password string) (auth.User, error)

	// RecordLogin records an SSO login time and avatar URL.
	RecordLogin(ctx context.Context, id int, avatar string) error

	// SetTwoFA sets or clears (empty typ) a user's 2FA type and key.
	SetTwoFA(ctx context.Context, id int, typ, key string) error

	// Delete deletes users; returns how many. ErrLastSuperAdmin (and
	// nothing deleted) if no enabled super admin would remain. The super
	// admin role is role ID 1 in upstream SQL; every backend's install
	// must give it that ID.
	Delete(ctx context.Context, ids []int) (int, error)

	// DeleteSessions logs a user out everywhere, except session exceptID
	// (empty = all).
	DeleteSessions(ctx context.Context, userID int, exceptID string) error
}

// SessionStore persists login sessions for simplesessions, in the same
// database (Postgres: upstream's simplesessions postgres store; SQLite:
// the same table and semantics). internal/auth takes it as a
// simplesessions.Store and never imports this package.
type SessionStore interface {
	simplesessions.Store

	// Prune deletes expired sessions (older than the TTL the store was
	// configured with, see Options.SessionTTL).
	Prune() error
}

// RoleStore is user roles (permission sets) and list roles (per-list
// get/manage permissions).
type RoleStore interface {
	// UserRoles returns user roles matching a name search. Sort.Field is
	// one of name, created_at, updated_at.
	UserRoles(ctx context.Context, search string, s Sort) ([]auth.Role, error)

	// UserRole returns one user role, or ErrNotFound.
	UserRole(ctx context.Context, id int) (auth.Role, error)

	// ListRoles returns list roles with their list permissions.
	ListRoles(ctx context.Context, search string, s Sort) ([]auth.ListRole, error)

	// CreateUserRole creates a user role; *ConflictError{Field: "name"}.
	CreateUserRole(ctx context.Context, r auth.Role) (auth.Role, error)

	// CreateListRole creates a list role with its list permissions
	// (r.Lists), atomically; *ConflictError{Field: "name"}.
	CreateListRole(ctx context.Context, r auth.ListRole) (auth.ListRole, error)

	// UpdateUserRole changes a user role's name and permissions;
	// ErrNotFound if missing (or if id is a per-list child role).
	UpdateUserRole(ctx context.Context, id int, r auth.Role) (auth.Role, error)

	// UpdateListRole changes a list role's name and replaces its list
	// permissions (lists not given are removed), atomically; upstream
	// does this in two statements without a transaction.
	UpdateListRole(ctx context.Context, id int, r auth.ListRole) (auth.ListRole, error)

	// DeleteListPermission removes one list from a list role; ErrInUse if
	// the database refuses it because users reference the role.
	DeleteListPermission(ctx context.Context, roleID, listID int) error

	// Delete deletes a role (a missing one is a no-op); ErrInUse if users
	// still have it.
	Delete(ctx context.Context, id int) error
}
