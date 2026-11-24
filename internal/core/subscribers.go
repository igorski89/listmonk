package core

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

const (
	optinThrottleTTL = 10 * time.Minute
	optinThrottleCap = 5000
)

// GetSubscriber fetches a subscriber by one of the given params.
func (c *Core) GetSubscriber(id int, uuid, email string) (models.Subscriber, error) {
	out, err := c.st.Subscribers().Get(bg, store.SubscriberRef{ID: id, UUID: uuid, Email: email})
	if errors.Is(err, store.ErrNotFound) {
		return models.Subscriber{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name",
				fmt.Sprintf("{globals.terms.subscriber} (%d: %s%s)", id, uuid, email)))
	}
	if err != nil {
		c.log.Printf("error fetching subscriber: %v", err)
		return models.Subscriber{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching",
				"name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// HasSubscriberLists checks if the given subscribers have at least one of the given lists.
func (c *Core) HasSubscriberLists(subIDs []int, listIDs []int) (map[int]bool, error) {
	out, err := c.st.Subscribers().HasLists(bg, subIDs, listIDs)
	if err != nil {
		c.log.Printf("error fetching subscriber: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetSubscribersByEmail fetches a subscriber by one of the given params.
func (c *Core) GetSubscribersByEmail(emails []string) (models.Subscribers, error) {
	out, err := c.st.Subscribers().GetByEmails(bg, emails)
	if err != nil {
		c.log.Printf("error fetching subscriber: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}
	if len(out) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("campaigns.noKnownSubsToTest"))
	}

	return out, nil
}

// QuerySubscribers queries and returns paginated subscrribers based on the given params including the total count.
func (c *Core) QuerySubscribers(searchStr, queryExp string, listIDs []int, subStatus, subscriberStatus string, order, orderBy string, offset, limit int) (models.Subscribers, int, error) {
	// Sort params.
	if !strSliceContains(orderBy, subQuerySortFields) {
		orderBy = ""
	}
	if order != models.OrderAsc && order != models.OrderDesc {
		order = models.OrderDesc
	}

	f := store.SubscriberFilter{Search: searchStr, Expr: queryExp, ListIDs: listIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	out, total, err := c.st.Subscribers().Query(bg, f, store.Sort{Field: orderBy, Desc: order == models.OrderDesc}, store.Page{Offset: offset, Limit: limit})
	if err != nil {
		var ie *store.InvalidQueryError
		if errors.As(err, &ie) {
			c.log.Printf("error validating query tables: %v", err)
			return nil, 0, echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("subscribers.errorPreparingQuery", "error", ie.Msg))
		}
		return nil, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return out, total, nil
}

// GetSubscriberLists returns a subscriber's lists based on the given conditions.
func (c *Core) GetSubscriberLists(subID int, uuid string, listIDs []int, listUUIDs []string, subStatus string, listType string) ([]models.List, error) {
	// Fetch double opt-in lists from the given list IDs.
	// Get the list of subscription lists where the subscriber hasn't confirmed.
	out, err := c.st.Subscribers().Lists(bg, store.SubscriberRef{ID: subID, UUID: uuid}, listIDs, listUUIDs, subStatus, listType)
	if err != nil {
		c.log.Printf("error fetching lists for opt-in: %s", pqErrMsg(err))
		return nil, err
	}

	return out, nil
}

// GetSubscriberProfileForExport returns the subscriber's profile data as a JSON exportable.
// Get the subscriber's data. A single query that gets the profile, list subscriptions, campaign views,
// and link clicks. Names of private lists are replaced with "Private list".
func (c *Core) GetSubscriberProfileForExport(id int, uuid string, allLists bool, listIDs []int) (models.SubscriberExportProfile, error) {
	out, err := c.st.Subscribers().Profile(bg, store.SubscriberRef{ID: id, UUID: uuid}, scopeOf(allLists, listIDs))
	if err != nil {
		c.log.Printf("error fetching subscriber export data: %v", err)

		return models.SubscriberExportProfile{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscribers}", "error", err.Error()))
	}

	return out, nil
}

// GetSubscriberActivity returns the subscriber's campaign views and link clicks for the Activity tab.
func (c *Core) GetSubscriberActivity(id int, allLists bool, listIDs []int) (models.SubscriberActivity, error) {
	out, err := c.st.Subscribers().Activity(bg, id, scopeOf(allLists, listIDs))
	if err != nil {
		c.log.Printf("error fetching subscriber activity: %v", err)

		return models.SubscriberActivity{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "activity", "error", err.Error()))
	}

	return out, nil
}

// GetSubscriberCharts returns subscriber views and clicks.
func (c *Core) GetSubscriberCharts(id int, allLists bool, listIDs []int) (types.JSONText, error) {
	out, err := c.st.Subscribers().Charts(bg, id, scopeOf(allLists, listIDs))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "subscriber charts", "error", pqErrMsg(err)))
	}
	return types.JSONText(out), nil
}

// DeleteSubscriberActivity deletes a subscriber's campaign views or link clicks.
func (c *Core) DeleteSubscriberActivity(id int, activityType string) error {
	var kind store.ActivityKind
	switch activityType {
	case "campaign_views":
		kind = store.ActivityCampaignViews
	case "link_clicks":
		kind = store.ActivityLinkClicks
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid activity type.")
	}

	if err := c.st.Subscribers().DeleteActivity(bg, id, kind); err != nil {
		c.log.Printf("error deleting subscriber activity: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.activity}", "error", pqErrMsg(err)))
	}

	return nil
}

// ExportSubscribers returns an iterator function that provides lists of subscribers based
// on the given criteria in an exportable form. The iterator function returned can be called
// repeatedly until there are nil subscribers. It's an iterator because exports can be extremely
// large and may have to be fetched in batches from the DB and streamed somewhere.
func (c *Core) ExportSubscribers(searchStr, query string, subIDs, listIDs []int, subStatus, subscriberStatus string, batchSize int) (func() ([]models.SubscriberExport, error), error) {
	f := store.SubscriberFilter{IDs: subIDs, Search: searchStr, Expr: query, ListIDs: listIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	next, err := c.st.Subscribers().Export(bg, f)
	if err != nil {
		var ie *store.InvalidQueryError
		if errors.As(err, &ie) {
			c.log.Printf("error validating query tables: %v", err)
			return nil, echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("subscribers.errorPreparingQuery", "error", ie.Msg))
		}
		c.log.Printf("error getting subscriber count: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	id := 0
	return func() ([]models.SubscriberExport, error) {
		out, err := next(id, batchSize)
		if err != nil {
			c.log.Printf("error exporting subscribers by query: %v", err)
			return nil, echo.NewHTTPError(http.StatusInternalServerError,
				c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
		}
		if len(out) == 0 {
			return nil, nil
		}

		id = out[len(out)-1].ID
		return out, nil
	}, nil
}

// InsertSubscriber inserts a subscriber and returns the ID. The first bool indicates if
// it was a new subscriber, and the second bool indicates if the subscriber was sent an optin confirmation.
// bool = optinSent?
func (c *Core) InsertSubscriber(sub models.Subscriber, listIDs []int, listUUIDs []string, preconfirm, assertOptin bool) (models.Subscriber, bool, error) {
	subStatus := models.SubscriptionStatusUnconfirmed
	if preconfirm {
		subStatus = models.SubscriptionStatusConfirmed
	}
	if sub.Status == "" {
		sub.Status = auth.UserStatusEnabled
	}

	out, err := c.st.Subscribers().Insert(bg,
		store.SubscriberInput{Email: sub.Email, Name: strings.TrimSpace(sub.Name), Status: sub.Status, Attribs: sub.Attribs},
		store.ListChange{ListIDs: listIDs, ListUUIDs: listUUIDs, Status: subStatus})
	if err != nil {
		var ce *store.ConflictError
		if errors.As(err, &ce) && ce.Field == "email" {
			return models.Subscriber{}, false, echo.NewHTTPError(http.StatusConflict, c.i18n.T("subscribers.emailExists"))
		}
		c.log.Printf("error inserting subscriber: %v", err)
		return models.Subscriber{}, false, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}

	hasOptin := false
	if !preconfirm && c.consts.SendOptinConfirmation {
		// Send a confirmation e-mail (if there are any double opt-in lists).
		num, err := c.sendOptinConfirmation(out, listIDs, assertOptin)
		if assertOptin && err != nil {
			return out, hasOptin, err
		}

		hasOptin = num > 0
	}

	return out, hasOptin, nil
}

// UpdateSubscriber updates a subscriber's properties.
func (c *Core) UpdateSubscriber(id int, sub models.Subscriber) (models.Subscriber, error) {
	out, err := c.st.Subscribers().Update(bg, id,
		store.SubscriberInput{Email: sub.Email, Name: strings.TrimSpace(sub.Name), Status: sub.Status, Attribs: sub.Attribs})
	if errors.Is(err, store.ErrNotFound) {
		return models.Subscriber{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name",
				fmt.Sprintf("{globals.terms.subscriber} (%d: %s%s)", id, "", "")))
	}
	if err != nil {
		c.log.Printf("error updating subscriber: %v", err)
		return models.Subscriber{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateSubscriberWithLists updates a subscriber's properties.
// If deleteLists is set to true, all existing subscriptions are deleted and only
// the ones provided are added or retained.
func (c *Core) UpdateSubscriberWithLists(id int, sub models.Subscriber, listIDs []int, listUUIDs []string, preconfirm, deleteLists, assertOptin bool, permittedListIDs []int, allowResubscribe bool) (models.Subscriber, bool, error) {
	subStatus := models.SubscriptionStatusUnconfirmed
	if preconfirm {
		subStatus = models.SubscriptionStatusConfirmed
	}

	out, err := c.st.Subscribers().UpdateWithLists(bg, id,
		store.SubscriberInput{Email: sub.Email, Name: strings.TrimSpace(sub.Name), Status: sub.Status, Attribs: sub.Attribs},
		store.ListChange{ListIDs: listIDs, ListUUIDs: listUUIDs, Status: subStatus, ReplaceExisting: deleteLists,
			// Upstream treats an empty (or nil) permitted list as "all lists".
			Permitted: scopeOf(len(permittedListIDs) == 0, permittedListIDs), AllowResubscribe: allowResubscribe})
	if errors.Is(err, store.ErrNotFound) {
		return models.Subscriber{}, false, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name",
				fmt.Sprintf("{globals.terms.subscriber} (%d: %s%s)", id, "", "")))
	}
	if err != nil {
		c.log.Printf("error updating subscriber: %v", err)
		return models.Subscriber{}, false, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscriber}", "error", pqErrMsg(err)))
	}

	hasOptin := false
	if !preconfirm && c.consts.SendOptinConfirmation {
		// Send a confirmation e-mail (if there are any double opt-in lists).
		num, err := c.sendOptinConfirmation(out, listIDs, assertOptin)
		if assertOptin && err != nil {
			return out, hasOptin, err
		}
		hasOptin = num > 0
	}

	return out, hasOptin, nil
}

// BlocklistSubscribers blocklists the given list of subscribers.
func (c *Core) BlocklistSubscribers(subIDs []int) error {
	if err := c.st.Subscribers().Blocklist(bg, subIDs); err != nil {
		c.log.Printf("error blocklisting subscribers: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("subscribers.errorBlocklisting", "error", err.Error()))
	}

	return nil
}

// BlocklistSubscribersByQuery blocklists the given list of subscribers.
func (c *Core) BlocklistSubscribersByQuery(searchStr, queryExp string, listIDs []int, subStatus, subscriberStatus string) error {
	f := store.SubscriberFilter{Search: searchStr, Expr: sanitizeSQLExp(queryExp), ListIDs: listIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	if _, err := c.st.Subscribers().BlocklistByFilter(bg, f); err != nil {
		c.log.Printf("error blocklisting subscribers: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("subscribers.errorBlocklisting", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteSubscribers deletes the given list of subscribers.
func (c *Core) DeleteSubscribers(subIDs []int, subUUIDs []string) error {
	if err := c.st.Subscribers().Delete(bg, subIDs, subUUIDs); err != nil {
		c.log.Printf("error deleting subscribers: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteSubscribersByQuery deletes subscribers by a given arbitrary query expression.
func (c *Core) DeleteSubscribersByQuery(searchStr, queryExp string, listIDs []int, subStatus, subscriberStatus string) error {
	f := store.SubscriberFilter{Search: searchStr, Expr: sanitizeSQLExp(queryExp), ListIDs: listIDs, SubscriptionStatus: subStatus, Status: subscriberStatus}
	_, err := c.st.Subscribers().DeleteByFilter(bg, f)
	if err != nil {
		c.log.Printf("error deleting subscribers: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return err
}

// UnsubscribeByCampaign unsubscribes a given subscriber from lists in a given campaign.
// If unsubUnconfirmed is true, unsub all unconfirmed subscriptions instead.
func (c *Core) UnsubscribeByCampaign(subUUID, campUUID string, blocklist, unsubAllUnconfirmed bool) error {
	if err := c.st.Subscribers().UnsubscribeByCampaign(bg, subUUID, campUUID, blocklist, unsubAllUnconfirmed); err != nil {
		c.log.Printf("error unsubscribing: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// ConfirmOptionSubscription confirms a subscriber's optin subscription.
func (c *Core) ConfirmOptionSubscription(subUUID string, listUUIDs []string, meta models.JSON) error {
	if err := c.st.Subscriptions().ConfirmOptin(bg, subUUID, listUUIDs, meta); err != nil {
		c.log.Printf("error confirming subscription: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteSubscriberBounces deletes the given list of subscribers.
func (c *Core) DeleteSubscriberBounces(id int, uuid string) error {
	if err := c.st.Bounces().DeleteForSubscriber(bg, store.SubscriberRef{ID: id, UUID: uuid}); err != nil {
		c.log.Printf("error deleting bounces: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.bounces}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteOrphanSubscribers deletes orphan subscriber records (subscribers without lists).
func (c *Core) DeleteOrphanSubscribers() (int, error) {
	n, err := c.st.Subscribers().DeleteOrphans(bg)
	if err != nil {
		c.log.Printf("error deleting orphan subscribers: %v", err)
		return 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return n, nil
}

// DeleteBlocklistedSubscribers deletes blocklisted subscribers.
func (c *Core) DeleteBlocklistedSubscribers() (int, error) {
	n, err := c.st.Subscribers().DeleteBlocklisted(bg)
	if err != nil {
		c.log.Printf("error deleting blocklisted subscribers: %v", err)
		return 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.subscribers}", "error", pqErrMsg(err)))
	}

	return n, nil
}

// sendOptinConfirmation throttles opt-in mails sent on public subscriptions.
func (c *Core) sendOptinConfirmation(sub models.Subscriber, listIDs []int, public bool) (int, error) {
	if public {
		var pending []int
		for _, l := range sub.ParsedLists() {
			if l.Optin == models.ListOptinDouble && l.SubscriptionStatus == models.SubscriptionStatusUnconfirmed {
				pending = append(pending, l.ID)
			}
		}
		if len(pending) == 0 {
			return 0, nil
		}

		// Make the throttle key from the e-mail + the normalized list of pending list IDs
		// so that new subscriptions don't get throttled.
		slices.Sort(pending)
		key := fmt.Sprintf("%s:%v", strings.ToLower(strings.TrimSpace(sub.Email)), pending)
		if !c.optinThrottle.Set(key) {
			return len(pending), nil
		}
		listIDs = pending
	}

	return c.h.SendOptinConfirmation(sub, listIDs)
}
