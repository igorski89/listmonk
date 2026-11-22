package core

import (
	"errors"
	"net/http"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

// GetRoles retrieves all roles.
func (c *Core) GetRoles(searchStr, orderBy, order string) ([]auth.Role, error) {
	if order == "" {
		order = models.OrderAsc
	}

	out, err := c.st.Roles().UserRoles(bg, makeSearchString(searchStr), sortOf(orderBy, order, roleQuerySortFields))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "role", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetRole retrieves a role.
func (c *Core) GetRole(id int) (auth.Role, error) {
	out, err := c.st.Roles().UserRole(bg, id)
	if err != nil {
		// Role does not exist.
		if errors.Is(err, store.ErrNotFound) {
			return auth.Role{}, echo.NewHTTPError(http.StatusInternalServerError,
				c.i18n.Ts("globals.messages.errorFetching", "name", "role", "error", "role not found"))
		}
		return auth.Role{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "role", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetListRoles retrieves all list roles.
func (c *Core) GetListRoles(searchStr, orderBy, order string) ([]auth.ListRole, error) {
	if order == "" {
		order = models.OrderAsc
	}

	out, err := c.st.Roles().ListRoles(bg, makeSearchString(searchStr), sortOf(orderBy, order, roleQuerySortFields))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "role", "error", pqErrMsg(err)))
	}

	return out, nil
}

// CreateRole creates a new role.
func (c *Core) CreateRole(r auth.Role) (auth.Role, error) {
	out, err := c.st.Roles().CreateUserRole(bg, r)
	if err != nil {
		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.role}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// CreateListRole creates a new list role.
func (c *Core) CreateListRole(r auth.ListRole) (auth.ListRole, error) {
	out, err := c.st.Roles().CreateListRole(bg, r)
	if err != nil {
		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.role}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// DeleteListPermission deletes a list permission entry from a role.
func (c *Core) DeleteListPermission(roleID, listID int) error {
	if err := c.st.Roles().DeleteListPermission(bg, roleID, listID); err != nil {
		if errors.Is(err, store.ErrInUse) {
			return echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("users.cantDeleteRole"))
		}

		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.role}", "error", pqErrMsg(err)))
	}

	return nil
}

// UpdateUserRole updates a given role.
func (c *Core) UpdateUserRole(id int, r auth.Role) (auth.Role, error) {
	out, err := c.st.Roles().UpdateUserRole(bg, id, r)
	if errors.Is(err, store.ErrNotFound) {
		return out, echo.NewHTTPError(http.StatusBadRequest, c.i18n.Ts("globals.messages.notFound", "name", "{users.userRole}"))
	}
	if err != nil {
		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{users.userRole}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateListRole updates a given role.
func (c *Core) UpdateListRole(id int, r auth.ListRole) (auth.ListRole, error) {
	out, err := c.st.Roles().UpdateListRole(bg, id, r)
	if errors.Is(err, store.ErrNotFound) {
		return out, echo.NewHTTPError(http.StatusBadRequest, c.i18n.Ts("globals.messages.notFound", "name", "{users.listRole}"))
	}
	if err != nil {
		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{users.listRole}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// DeleteRole deletes a given role.
func (c *Core) DeleteRole(id int) error {
	if err := c.st.Roles().Delete(bg, id); err != nil {
		if errors.Is(err, store.ErrInUse) {
			return echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("users.cantDeleteRole"))
		}

		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.role}", "error", pqErrMsg(err)))
	}

	return nil
}
