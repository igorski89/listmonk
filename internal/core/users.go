package core

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/utils"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
	"gopkg.in/volatiletech/null.v6"
)

// GetUsers retrieves users.
func (c *Core) GetUsers(typ, status string, userRoleID, listRoleID int, orderBy, order string) ([]auth.User, error) {
	if order == "" {
		order = models.OrderAsc
	}

	out, err := c.st.Users().Query(bg, store.UserFilter{Type: typ, Status: status, UserRoleID: userRoleID, ListRoleID: listRoleID},
		sortOf(orderBy, order, userQuerySortFields))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.users}", "error", pqErrMsg(err)))
	}

	return c.setupUserFields(out), nil
}

// GetUser retrieves a specific user based on any one given identifier.
func (c *Core) GetUser(id int, username, email string) (auth.User, error) {
	out, err := c.st.Users().Get(bg, store.UserRef{ID: id, Username: username, Email: email})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, echo.NewHTTPError(http.StatusNotFound,
				c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.user}"))
		}

		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.users}", "error", pqErrMsg(err)))
	}

	return c.setupUserFields([]auth.User{out})[0], nil
}

// CreateUser creates a new user.
func (c *Core) CreateUser(u auth.User) (auth.User, error) {
	var (
		apiToken   string
		dbPassword = u.Password
	)

	// If it's an API user, generate a random token for password
	// and set the e-mail to default.
	if u.Type == auth.UserTypeAPI {
		// Generate a random API token.
		tk, err := utils.GenerateRandomString(48)
		if err != nil {
			return auth.User{}, err
		}
		apiToken = tk

		u.Email = null.String{String: u.Username + "@api", Valid: true}
		u.PasswordLogin = false
		u.Password = null.String{String: tk, Valid: true}
		dbPassword = null.String{String: auth.HashAPIToken(tk), Valid: true}
	}

	dbUser := u
	dbUser.Password = dbPassword
	id, err := c.st.Users().Create(bg, dbUser)
	if err != nil {
		return auth.User{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	// Hide the password field in the response except for when the user type is an API token,
	// where the frontend shows the token on the UI just once.
	if u.Type != auth.UserTypeAPI {
		u.Password = null.String{Valid: false}
	}

	out, err := c.GetUser(id, "", "")
	if u.Type == auth.UserTypeAPI {
		out.Password = null.String{String: apiToken, Valid: true}
	}

	return out, err
}

// UpdateUser updates a given user.
func (c *Core) UpdateUser(id int, u auth.User) (auth.User, error) {
	if err := c.st.Users().Update(bg, id, u); err != nil {
		if errors.Is(err, store.ErrLastSuperAdmin) || errors.Is(err, store.ErrNotFound) {
			return auth.User{}, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("users.needSuper"))
		}
		return auth.User{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	out, err := c.GetUser(id, "", "")

	return out, err
}

// UpdateUserProfile updates the basic fields of a given uesr (name, email, password).
func (c *Core) UpdateUserProfile(id int, u auth.User) (auth.User, error) {
	if err := c.st.Users().UpdateProfile(bg, id, u); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.User{}, echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.user}"))
		}
		return auth.User{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	return c.GetUser(id, "", "")
}

// UpdateUserLogin updates a user's record post-login.
func (c *Core) UpdateUserLogin(id int, avatar string) error {
	if err := c.st.Users().RecordLogin(bg, id, avatar); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	return nil
}

// SetTwoFA sets or clears the 2FA configuration for a user.
func (c *Core) SetTwoFA(id int, twofaType, twofaKey string) error {
	if err := c.st.Users().SetTwoFA(bg, id, twofaType, twofaKey); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteUserSessions deletes all sessions for a given user ID, optionally
// excluding a specific session ID (to keep the current session alive).
func (c *Core) DeleteUserSessions(userID int, excludeID string) error {
	return c.st.Users().DeleteSessions(bg, userID, excludeID)
}

// DeleteUsers deletes a given user.
func (c *Core) DeleteUsers(ids []int) error {
	if _, err := c.st.Users().Delete(bg, ids); err != nil {
		if errors.Is(err, store.ErrLastSuperAdmin) {
			return echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("users.needSuper"))
		}
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.user}", "error", pqErrMsg(err)))
	}

	return nil
}

// LoginUser attempts to log the given user_id in by matching the password.
func (c *Core) LoginUser(username, password string) (auth.User, error) {
	out, err := c.st.Users().Login(bg, username, password)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, echo.NewHTTPError(http.StatusForbidden, c.i18n.T("users.invalidLogin"))
		}

		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.users}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// setupUserFields prepares and sets up various user fields.
func (c *Core) setupUserFields(users []auth.User) []auth.User {
	for n, u := range users {
		u := u

		if u.Password.String != "" {
			u.HasPassword = true
			u.PasswordLogin = true
		}

		if u.Type == auth.UserTypeAPI {
			u.Email = null.String{}
		}

		u.UserRole.ID = u.UserRoleID
		u.UserRole.Name = u.UserRoleName
		u.UserRole.Permissions = u.UserRolePerms
		u.UserRoleID = 0

		// Prepare lookup maps.
		u.ListPermissionsMap = make(map[int]map[string]struct{})
		u.PermissionsMap = make(map[string]struct{})
		for _, p := range u.UserRolePerms {
			u.PermissionsMap[p] = struct{}{}
		}

		if u.ListRoleID != nil {
			// Unmarshall the raw list perms map.
			var listPerms []auth.ListPermission
			if u.ListsPermsRaw != nil {
				if err := json.Unmarshal(*u.ListsPermsRaw, &listPerms); err != nil {
					c.log.Printf("error unmarshalling list permissions for role %d: %v", u.ID, err)
				}
			}

			u.ListRole = &auth.ListRolePermissions{ID: *u.ListRoleID, Name: u.ListRoleName.String, Lists: listPerms}

			// Iterate each list in the list permissions and setup get/manage list IDs.
			for _, p := range listPerms {
				u.ListPermissionsMap[p.ID] = make(map[string]struct{})

				for _, perm := range p.Permissions {
					u.ListPermissionsMap[p.ID][perm] = struct{}{}

					// List IDs with get / manage permissions.
					if perm == auth.PermListGet {
						u.GetListIDs = append(u.GetListIDs, p.ID)
					}
					if perm == auth.PermListManage {
						u.ManageListIDs = append(u.ManageListIDs, p.ID)
					}
				}
			}
		}

		users[n] = u
	}

	return users
}
