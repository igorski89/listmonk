package core

import (
	"errors"
	"net/http"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

var bounceQuerySortFields = []string{"email", "campaign_name", "source", models.FieldCreatedAt, "type"}

// QueryBounces retrieves paginated bounce entries based on the given params.
// It also returns the total number of bounce records within the permitted lists.
func (c *Core) QueryBounces(campID, subID int, source, typ, orderBy, order string, offset, limit int, allLists bool, listIDs []int) ([]models.Bounce, int, error) {
	if !strSliceContains(orderBy, bounceQuerySortFields) {
		orderBy = models.FieldCreatedAt
	}
	if order != models.OrderAsc && order != models.OrderDesc {
		order = models.OrderDesc
	}

	f := store.BounceFilter{CampaignID: campID, SubscriberID: subID, Source: source, Type: typ, Scope: scopeOf(allLists, listIDs)}
	out, total, err := c.st.Bounces().Query(bg, f, store.Sort{Field: orderBy, Desc: order == models.OrderDesc}, store.Page{Offset: offset, Limit: limit})
	if err != nil {
		c.log.Printf("error fetching bounces: %v", err)
		return nil, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.bounce}", "error", pqErrMsg(err)))
	}

	return out, total, nil
}

// GetBounce retrieves bounce entries based on the given params.
func (c *Core) GetBounce(id int, allLists bool, listIDs []int) (models.Bounce, error) {
	out, err := c.st.Bounces().Get(bg, id, scopeOf(allLists, listIDs))
	if errors.Is(err, store.ErrNotFound) {
		return models.Bounce{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.bounce}"))
	}
	if err != nil {
		c.log.Printf("error fetching bounces: %v", err)
		return models.Bounce{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.bounce}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// RecordBounce records a new bounce.
func (c *Core) RecordBounce(b models.Bounce) error {
	action, ok := c.consts.BounceActions[b.Type]
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, c.i18n.Ts("globals.messages.invalidData")+": "+b.Type)
	}

	err := c.st.Bounces().Record(bg, b, store.BounceAction{Count: action.Count, Action: action.Action})
	if err != nil {
		// Ignore the error if it complained of no subscriber.
		if errors.Is(err, store.ErrNotFound) {
			c.log.Printf("bounced subscriber (%s / %s) not found", b.SubscriberUUID, b.Email)
			return nil
		}

		c.log.Printf("error recording bounce: %v", err)
	}

	return err
}

// BlocklistBouncedSubscribers blocklists subscribers matching bounce IDs or filters.
func (c *Core) BlocklistBouncedSubscribers(ids []int, campID int, source, typ string, allLists bool, listIDs []int) error {
	f := store.BounceFilter{IDs: ids, All: len(ids) == 0, CampaignID: campID, Source: source, Type: typ, Scope: scopeOf(allLists, listIDs)}
	if err := c.st.Bounces().BlocklistSubscribers(bg, f); err != nil {
		c.log.Printf("error blocklisting bounced subscribers: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("subscribers.errorBlocklisting", "error", err.Error()))
	}

	return nil
}

// DeleteBounce deletes a bounce record.
func (c *Core) DeleteBounce(id int, allLists bool, listIDs []int) error {
	return c.DeleteBounces([]int{id}, false, 0, "", "", allLists, listIDs)
}

// DeleteBounces deletes bounce records by IDs or filters.
func (c *Core) DeleteBounces(ids []int, all bool, campID int, source, typ string, allLists bool, listIDs []int) error {
	f := store.BounceFilter{IDs: ids, All: all, CampaignID: campID, Source: source, Type: typ, Scope: scopeOf(allLists, listIDs)}
	if _, err := c.st.Bounces().Delete(bg, f); err != nil {
		c.log.Printf("error deleting lists: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.list}", "error", pqErrMsg(err)))
	}

	return nil
}
