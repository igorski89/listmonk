package core

import (
	"net/http"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

// GetSubscriptions retrieves the subscriptions for a subscriber.
func (c *Core) GetSubscriptions(subID int, subUUID string, allLists bool) ([]models.Subscription, error) {
	out, err := c.st.Subscriptions().List(bg, store.SubscriberRef{ID: subID, UUID: subUUID}, allLists)
	if err != nil {
		c.log.Printf("error getting subscriptions: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscribers}", "error", err.Error()))
	}

	return out, err
}

// AddSubscriptions adds list subscriptions to subscribers.
func (c *Core) AddSubscriptions(subIDs, listIDs []int, status string) error {
	if err := c.st.Subscriptions().Add(bg, subIDs, listIDs, status); err != nil {
		c.log.Printf("error adding subscriptions: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", err.Error()))
	}

	return nil
}

// AddSubscriptionsByQuery adds list subscriptions to subscribers by a given arbitrary query expression.
// sourceListIDs is the list of list IDs to filter the subscriber query with.
func (c *Core) AddSubscriptionsByQuery(searchStr, queryExp string, sourceListIDs, targetListIDs []int, status string, subStatus, subscriberStatus string) error {
	if sourceListIDs == nil {
		sourceListIDs = []int{}
	}

	f := store.SubscriberFilter{Search: searchStr, Expr: queryExp, ListIDs: sourceListIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	_, err := c.st.Subscriptions().AddByFilter(bg, f, targetListIDs, status)
	if err != nil {
		c.log.Printf("error adding subscriptions by query: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteSubscriptions delete list subscriptions from subscribers.
func (c *Core) DeleteSubscriptions(subIDs, listIDs []int) error {
	if err := c.st.Subscriptions().Remove(bg, subIDs, listIDs); err != nil {
		c.log.Printf("error deleting subscriptions: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", err.Error()))

	}

	return nil
}

// DeleteSubscriptionsByQuery deletes list subscriptions from subscribers by a given arbitrary query expression.
// sourceListIDs is the list of list IDs to filter the subscriber query with.
func (c *Core) DeleteSubscriptionsByQuery(searchStr, queryExp string, sourceListIDs, targetListIDs []int, subStatus, subscriberStatus string) error {
	if sourceListIDs == nil {
		sourceListIDs = []int{}
	}

	f := store.SubscriberFilter{Search: searchStr, Expr: queryExp, ListIDs: sourceListIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	_, err := c.st.Subscriptions().RemoveByFilter(bg, f, targetListIDs)
	if err != nil {
		c.log.Printf("error deleting subscriptions by query: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// UnsubscribeLists sets list subscriptions to 'unsubscribed'.
func (c *Core) UnsubscribeLists(subIDs, listIDs []int, listUUIDs []string) error {
	if err := c.st.Subscriptions().Unsubscribe(bg, subIDs, listIDs, listUUIDs); err != nil {
		c.log.Printf("error unsubscribing from lists: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", err.Error()))
	}

	return nil
}

// UnsubscribeListsByQuery sets list subscriptions to 'unsubscribed' by a given arbitrary query expression.
// sourceListIDs is the list of list IDs to filter the subscriber query with.
func (c *Core) UnsubscribeListsByQuery(searchStr, queryExp string, sourceListIDs, targetListIDs []int, subStatus, subscriberStatus string) error {
	if sourceListIDs == nil {
		sourceListIDs = []int{}
	}

	f := store.SubscriberFilter{Search: searchStr, Expr: queryExp, ListIDs: sourceListIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	_, err := c.st.Subscriptions().UnsubscribeByFilter(bg, f, targetListIDs)
	if err != nil {
		c.log.Printf("error unsubscribing from lists by query: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteUnconfirmedSubscriptions sets list subscriptions to 'unsubscribed' by a given arbitrary query expression.
// sourceListIDs is the list of list IDs to filter the subscriber query with.
func (c *Core) DeleteUnconfirmedSubscriptions(beforeDate time.Time) (int, error) {
	n, err := c.st.Subscriptions().DeleteUnconfirmed(bg, beforeDate)
	if err != nil {
		c.log.Printf("error deleting unconfirmed subscribers: %v", err)
		return 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return n, nil
}
