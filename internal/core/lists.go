package core

import (
	"errors"
	"net/http"

	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

// GetLists gets all lists optionally filtered by type and status.
func (c *Core) GetLists(typ, status string, getAll bool, permittedIDs []int) ([]models.List, error) {
	out, err := c.st.Lists().All(bg, typ, status, scopeOf(getAll, permittedIDs))
	if err != nil {
		c.log.Printf("error fetching lists: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.lists}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// QueryLists gets multiple lists based on multiple query params. Along with the  paginated and sliced
// results, the total number of lists in the DB is returned.
func (c *Core) QueryLists(searchStr, typ, optin, status string, tags []string, orderBy, order string, getAll bool, permittedIDs []int, offset, limit int) ([]models.List, int, error) {
	if orderBy == "" {
		orderBy = models.FieldID
	}
	if order == "" {
		order = models.OrderAsc
	}

	f := store.ListFilter{Search: makeSearchString(searchStr), Type: typ, Optin: optin, Status: status, Tags: tags, Scope: scopeOf(getAll, permittedIDs)}
	out, total, err := c.st.Lists().Query(bg, f, sortOf(orderBy, order, listQuerySortFields), store.Page{Offset: offset, Limit: limit})
	if err != nil {
		c.log.Printf("error fetching lists: %v", err)
		return nil, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.lists}", "error", pqErrMsg(err)))
	}

	return out, total, nil
}

// GetList gets a list by its ID or UUID.
func (c *Core) GetList(id int, uuid string) (models.List, error) {
	out, err := c.st.Lists().Get(bg, store.ListRef{ID: id, UUID: uuid}, store.ListScope{All: true})
	if errors.Is(err, store.ErrNotFound) {
		return models.List{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.list}"))
	}
	if err != nil {
		c.log.Printf("error fetching lists: %v", err)
		return models.List{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.lists}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetListsByOptin returns lists by optin type.
func (c *Core) GetListsByOptin(ids []int, optinType string) ([]models.List, error) {
	out, err := c.st.Lists().ByOptin(bg, ids, optinType)
	if err != nil {
		c.log.Printf("error fetching lists for opt-in: %s", pqErrMsg(err))
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.list}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetListTypes returns lists by their IDs or UUIDs.
// If ids is given, then the map returned has the list IDs as keys,
// otherwise, they have UUIDs as the keys.
// Note: This is a really weird and awkward API. Ideally, Go Generics
// should've somehow supported generic struct methods.
func (c *Core) GetListTypes(ids []int, uuids []string) (map[any]string, error) {
	out, err := c.st.Lists().Types(bg, ids, uuids)
	if err != nil {
		c.log.Printf("error fetching list types: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.list}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// CreateList creates a new list.
func (c *Core) CreateList(l models.List) (models.List, error) {
	if l.Type == "" {
		l.Type = models.ListTypePrivate
	}
	if l.Optin == "" {
		l.Optin = models.ListOptinSingle
	}
	if l.Status == "" {
		l.Status = models.ListStatusActive
	}

	l.Tags = normalizeTags(l.Tags)
	out, err := c.st.Lists().Create(bg, l)
	if err != nil {
		c.log.Printf("error creating list: %v", err)
		return models.List{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.list}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateList updates a given list.
func (c *Core) UpdateList(id int, l models.List) (models.List, error) {
	l.Tags = normalizeTags(l.Tags)
	out, err := c.st.Lists().Update(bg, id, l)
	if errors.Is(err, store.ErrNotFound) {
		return models.List{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.list}"))
	}
	if err != nil {
		c.log.Printf("error updating list: %v", err)
		return models.List{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.list}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// DeleteList deletes a list.
func (c *Core) DeleteList(id int) error {
	return c.DeleteLists([]int{id}, "", "", "", "", nil, true, nil)
}

// DeleteLists deletes multiple lists.
func (c *Core) DeleteLists(ids []int, query, typ, optin, status string, tags []string, getAll bool, permittedIDs []int) error {
	var queryStr string
	if len(ids) == 0 {
		queryStr = makeSearchString(query)
	}

	f := store.ListFilter{IDs: ids, Search: queryStr, Type: typ, Optin: optin, Status: status, Tags: tags, Scope: scopeOf(getAll, permittedIDs)}
	if _, err := c.st.Lists().DeleteMany(bg, f); err != nil {
		c.log.Printf("error deleting lists: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.lists}", "error", pqErrMsg(err)))
	}

	return nil
}

// GetListCharts returns weekly subscription, unsubscription, and campaign view stats.
func (c *Core) GetListCharts(id int, campaignViews bool) (types.JSONText, error) {
	out, err := c.st.Stats().ListCharts(bg, id, campaignViews)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "list charts", "error", pqErrMsg(err)))
	}

	return types.JSONText(out), nil
}

// GetListAnalyticsCounts returns subscriptions or campaign events grouped by list.
func (c *Core) GetListAnalyticsCounts(ids []int, typ, fromDate, toDate, interval string) ([]models.CampaignAnalyticsCount, error) {
	switch typ {
	case models.AnalyticsTypeSubscriptions, models.AnalyticsTypeUnsubscriptions, models.AnalyticsTypeViews,
		models.AnalyticsTypeClicks, models.AnalyticsTypeTotalViews, models.AnalyticsTypeTotalClicks, models.AnalyticsTypeBounces:
	default:
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("globals.messages.invalidData"))
	}

	return c.getAnalyticsCounts(store.AnalyticsLists, typ, ids, fromDate, toDate, interval)
}

// GetListAnalyticsLinks returns link counts across campaigns by lists.
func (c *Core) GetListAnalyticsLinks(ids []int, fromDate, toDate string) ([]models.CampaignAnalyticsLink, error) {
	return c.getAnalyticsLinks(store.AnalyticsLists, ids, fromDate, toDate)
}
