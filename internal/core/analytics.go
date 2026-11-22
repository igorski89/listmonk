package core

import (
	"net/http"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

// GetAnalyticsItems fetches campaign analytics datapoints for items (by: lists/campaigns) in the given order.
func (c *Core) GetAnalyticsItems(resource string, ids []int, getAll bool, permittedLists []int) ([]models.AnalyticsItem, error) {
	out := []models.AnalyticsItem{}
	if len(ids) == 0 {
		return out, nil
	}

	subject := store.AnalyticsLists
	switch resource {
	case models.ResourceLists:
	case models.ResourceCampaigns:
		subject = store.AnalyticsCampaigns
	default:
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("globals.messages.invalidData"))
	}

	out, err := c.st.Analytics().Items(bg, subject, ids, scopeOf(getAll, permittedLists))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// getAnalyticsCounts validates the range and interval for campaign and list charts.
func (c *Core) getAnalyticsCounts(subject store.AnalyticsSubject, metric string, ids []int, fromDate, toDate, interval string) ([]models.CampaignAnalyticsCount, error) {
	if !strHasLen(fromDate, 10, 30) || !strHasLen(toDate, 10, 30) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("analytics.invalidDates"))
	}

	switch interval {
	case models.AnalyticsIntervalHour, models.AnalyticsIntervalDay, models.AnalyticsIntervalWeek, models.AnalyticsIntervalMonth:
	default:
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("globals.messages.invalidData"))
	}

	out, err := c.st.Analytics().Counts(bg, store.AnalyticsQuery{Subject: subject, IDs: ids, Metric: metric, From: fromDate, To: toDate, Interval: interval})
	if err != nil {
		c.log.Printf("error fetching analytics: %v", err)

		return nil, echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
	}

	return out, nil
}

func (c *Core) getAnalyticsLinks(subject store.AnalyticsSubject, ids []int, fromDate, toDate string) ([]models.CampaignAnalyticsLink, error) {
	out, err := c.st.Analytics().Links(bg, subject, ids, fromDate, toDate)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetCampaignActivity returns clicks, views activity for a given set of campaigns/lists.
func (c *Core) GetCampaignActivity(ids []int, from, to string, allLists bool, listIDs []int, offset, limit int) ([]models.CampaignActivity, error) {
	out, err := c.st.Analytics().Activity(bg, ids, from, to, scopeOf(allLists, listIDs), store.Page{Offset: offset, Limit: limit})
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetCampaignActivityEvents returns clicks, views activity for a given set of campaigns/lists for a subscriber.
func (c *Core) GetCampaignActivityEvents(ids []int, from, to string, subscriberID, offset, limit int) ([]models.CampaignActivityEvent, error) {
	out, err := c.st.Analytics().ActivityEvents(bg, ids, from, to, subscriberID, store.Page{Offset: offset, Limit: limit})
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
	}

	return out, nil
}
