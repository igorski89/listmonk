package core

import (
	"errors"
	"net/http"
	"time"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

const (
	CampaignAnalyticsViews   = "views"
	CampaignAnalyticsClicks  = "clicks"
	CampaignAnalyticsBounces = "bounces"

	campaignTplDefault = "default"
	campaignTplArchive = "archive"
)

// QueryCampaigns retrieves paginated campaigns optionally filtering them by the given arbitrary
// query expression. It also returns the total number of records in the DB.
func (c *Core) QueryCampaigns(searchStr string, statuses, tags []string, typ, orderBy, order string, getAll bool, permittedLists []int, offset, limit int) (models.Campaigns, int, error) {
	f := store.CampaignFilter{Search: makeSearchString(searchStr), Statuses: statuses, Tags: tags, Type: typ, Scope: scopeOf(getAll, permittedLists)}
	out, total, err := c.st.Campaigns().Query(bg, f, sortOf(orderBy, order, campQuerySortFields), store.Page{Offset: offset, Limit: limit})
	if err != nil {
		c.log.Printf("error fetching campaigns: %v", err)
		return nil, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, total, nil
}

// GetCampaign retrieves a campaign.
func (c *Core) GetCampaign(id int, uuid, archiveSlug string) (models.Campaign, error) {
	return c.getCampaign(id, uuid, archiveSlug, campaignTplDefault)
}

// GetArchivedCampaign retrieves a campaign with the archive template body.
func (c *Core) GetArchivedCampaign(id int, uuid, archiveSlug string) (models.Campaign, error) {
	out, err := c.getCampaign(id, uuid, archiveSlug, campaignTplArchive)
	if err != nil {
		return out, err
	}

	if !out.Archive || out.Type != models.CampaignTypeRegular ||
		(out.Status != models.CampaignStatusRunning && out.Status != models.CampaignStatusPaused && out.Status != models.CampaignStatusFinished) {
		return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
	}

	return out, nil
}

// getCampaign retrieves a campaign. If typlType=default, then the campaign's
// template body is returned as "template_body". If tplType="archive",
// the archive template is returned.
func (c *Core) getCampaign(id int, uuid, archiveSlug string, tplType string) (models.Campaign, error) {
	out, err := c.st.Campaigns().Get(bg, store.CampaignRef{ID: id, UUID: uuid, ArchiveSlug: archiveSlug}, store.TemplateKind(tplType))
	if errors.Is(err, store.ErrNotFound) {
		return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
	}
	if err != nil {
		c.log.Printf("error fetching campaign: %v", err)
		return models.Campaign{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetCampaignForPreview retrieves a campaign with a template body. If the optional tplID is > 0
// that particular template is used, otherwise, the template saved on the campaign is.
func (c *Core) GetCampaignForPreview(id, tplID int) (models.Campaign, error) {
	out, err := c.st.Campaigns().GetForPreview(bg, id, tplID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
		}

		c.log.Printf("error fetching campaign: %v", err)
		return models.Campaign{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetArchivedCampaigns retrieves campaigns with a template body.
func (c *Core) GetArchivedCampaigns(offset, limit int, listUUID string) (models.Campaigns, int, error) {
	out, total, err := c.st.Campaigns().Archived(bg, listUUID, store.Page{Offset: offset, Limit: limit})
	if err != nil {
		c.log.Printf("error fetching public campaigns: %v", err)
		return models.Campaigns{}, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, total, nil
}

// CreateCampaign creates a new campaign.
func (c *Core) CreateCampaign(o models.Campaign, listIDs []int, mediaIDs []int) (models.Campaign, error) {
	o.Tags = normalizeTags(o.Tags)
	out, err := c.st.Campaigns().Create(bg, o, listIDs, mediaIDs)
	if err != nil {
		c.log.Printf("error creating campaign: %v", err)
		return models.Campaign{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateCampaign updates a campaign.
func (c *Core) UpdateCampaign(id int, o models.Campaign, listIDs []int, mediaIDs []int) (models.Campaign, error) {
	o.Tags = normalizeTags(o.Tags)
	out, err := c.st.Campaigns().Update(bg, id, o, listIDs, mediaIDs)
	if errors.Is(err, store.ErrNotFound) {
		return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
	}
	if err != nil {
		c.log.Printf("error updating campaign: %v", err)
		return models.Campaign{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateCampaignStatus updates a campaign's status, eg: draft to running.
func (c *Core) UpdateCampaignStatus(id int, status string) (models.Campaign, error) {
	cm, err := c.GetCampaign(id, "", "")
	if err != nil {
		return models.Campaign{}, err
	}

	errMsg := ""
	switch status {
	case models.CampaignStatusDraft:
		if cm.Status != models.CampaignStatusScheduled {
			errMsg = c.i18n.T("campaigns.onlyScheduledAsDraft")
		}
	case models.CampaignStatusScheduled:
		if cm.Status != models.CampaignStatusDraft && cm.Status != models.CampaignStatusPaused {
			errMsg = c.i18n.T("campaigns.onlyDraftAsScheduled")
		}
		if !cm.SendAt.Valid {
			errMsg = c.i18n.T("campaigns.needsSendAt")
		}

	case models.CampaignStatusRunning:
		if cm.Status != models.CampaignStatusPaused && cm.Status != models.CampaignStatusDraft {
			errMsg = c.i18n.T("campaigns.onlyPausedDraft")
		}
	case models.CampaignStatusPaused:
		if cm.Status != models.CampaignStatusRunning {
			errMsg = c.i18n.T("campaigns.onlyActivePause")
		}
	case models.CampaignStatusCancelled:
		if cm.Status != models.CampaignStatusRunning && cm.Status != models.CampaignStatusPaused {
			errMsg = c.i18n.T("campaigns.onlyActiveCancel")
		}
	}

	if len(errMsg) > 0 {
		return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest, errMsg)
	}

	if err := c.st.Campaigns().SetStatus(bg, cm.ID, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return models.Campaign{}, echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
		}
		c.log.Printf("error updating campaign status: %v", err)

		return models.Campaign{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	cm.Status = status
	return cm, nil
}

// UpdateCampaignArchive updates a campaign's archive properties.
func (c *Core) UpdateCampaignArchive(id int, enabled bool, tplID int, meta models.JSON, archiveSlug string) error {
	if err := c.st.Campaigns().SetArchive(bg, id, store.ArchiveSettings{Enabled: enabled, Slug: archiveSlug, TemplateID: tplID, Meta: meta}); err != nil {
		c.log.Printf("error updating campaign: %v", err)

		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteCampaign deletes a campaign.
func (c *Core) DeleteCampaign(id int) error {
	if err := c.st.Campaigns().Delete(bg, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return echo.NewHTTPError(http.StatusBadRequest,
				c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.campaign}"))
		}
		c.log.Printf("error deleting campaign: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteCampaigns deletes multiple campaigns by IDs or by query.
func (c *Core) DeleteCampaigns(ids []int, query string, statuses, tags []string, typ string, hasAllPerm bool, permittedLists []int) error {
	var queryStr string
	if len(ids) == 0 {
		queryStr = makeSearchString(query)
	}

	f := store.CampaignFilter{IDs: ids, Search: queryStr, Statuses: statuses, Tags: tags, Type: typ, Scope: scopeOf(hasAllPerm, permittedLists)}
	if _, err := c.st.Campaigns().DeleteMany(bg, f); err != nil {
		c.log.Printf("error deleting campaigns: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.campaigns}", "error", pqErrMsg(err)))
	}

	return nil
}

// CampaignHasLists checks if a campaign has any of the given list IDs.
func (c *Core) CampaignHasLists(id int, listIDs []int) (bool, error) {
	has, err := c.st.Campaigns().HasLists(bg, id, listIDs)
	if err != nil {
		c.log.Printf("error checking campaign lists: %v", err)
		return false, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return has, nil
}

// GetRunningCampaignStats returns the progress stats of running campaigns.
func (c *Core) GetRunningCampaignStats(hasAllPerm bool, permittedLists []int) ([]models.CampaignStats, error) {
	out, err := c.st.Campaigns().RunningStats(bg, scopeOf(hasAllPerm, permittedLists))
	if err != nil {
		c.log.Printf("error fetching campaign stats: %v", err)
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}

	return out, nil
}

func (c *Core) GetCampaignAnalyticsCounts(campIDs []int, typ, fromDate, toDate, interval string) ([]models.CampaignAnalyticsCount, error) {
	// Pick campaign view counts or click counts.
	switch typ {
	case models.AnalyticsTypeViews, models.AnalyticsTypeClicks, models.AnalyticsTypeTotalViews,
		models.AnalyticsTypeTotalClicks, models.AnalyticsTypeBounces:
	default:
		return nil, echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("globals.messages.invalidData"))
	}

	return c.getAnalyticsCounts(store.AnalyticsCampaigns, typ, campIDs, fromDate, toDate, interval)
}

// GetCampaignAnalyticsLinks returns link click analytics for the given campaign IDs.
func (c *Core) GetCampaignAnalyticsLinks(ids []int, fromDate, toDate string) ([]models.CampaignAnalyticsLink, error) {
	return c.getAnalyticsLinks(store.AnalyticsCampaigns, ids, fromDate, toDate)
}

// RegisterCampaignView registers a subscriber's view on a campaign.
func (c *Core) RegisterCampaignView(campUUID, subUUID string) error {
	if err := c.st.Tracking().RegisterView(bg, campUUID, subUUID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}

		c.log.Printf("error registering campaign view: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.campaign}", "error", pqErrMsg(err)))
	}
	return nil
}

// GetLinkURL returns the original URL for a link UUID without recording a click.
func (c *Core) GetLinkURL(linkUUID string) (string, error) {
	url, err := c.st.Tracking().LinkURL(bg, linkUUID)
	if err != nil {
		c.log.Printf("error getting link URL: %s", err)
		return "", echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("public.errorProcessingRequest"))
	}
	return url, nil
}

// RegisterCampaignLinkClick registers a subscriber's link click on a campaign.
func (c *Core) RegisterCampaignLinkClick(linkUUID, campUUID, subUUID string) (string, error) {
	url, err := c.st.Tracking().RegisterClick(bg, linkUUID, campUUID, subUUID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", echo.NewHTTPError(http.StatusBadRequest, c.i18n.Ts("globals.messages.invalidLink"))
		}

		c.log.Printf("error registering link click: %s", err)
		return "", echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("public.errorProcessingRequest"))
	}

	return url, nil
}

// ExportCampaignViews returns an iterator with campaign views for streaming/exporting.
func (c *Core) ExportCampaignViews(since time.Time, batchSize int) func() ([]models.CampaignViewExport, error) {
	offset := 0
	return func() ([]models.CampaignViewExport, error) {
		out, err := c.st.Tracking().ExportViews(bg, since, store.Page{Offset: offset, Limit: batchSize})
		if err != nil {
			c.log.Printf("error exporting campaign views: %v", err)
			return nil, echo.NewHTTPError(http.StatusInternalServerError,
				c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
		}
		offset += len(out)
		return out, nil
	}
}

// ExportCampaignLinkClicks returns an iterator with campaign link click for streaming/exporting.
func (c *Core) ExportCampaignLinkClicks(since time.Time, batchSize int) func() ([]models.CampaignClickExport, error) {
	offset := 0
	return func() ([]models.CampaignClickExport, error) {
		out, err := c.st.Tracking().ExportClicks(bg, since, store.Page{Offset: offset, Limit: batchSize})
		if err != nil {
			c.log.Printf("error exporting campaign link clicks: %v", err)
			return nil, echo.NewHTTPError(http.StatusInternalServerError,
				c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.analytics}", "error", pqErrMsg(err)))
		}
		offset += len(out)
		return out, nil
	}
}

// DeleteCampaignViews deletes campaign views older than a given date.
func (c *Core) DeleteCampaignViews(before time.Time) error {
	if _, err := c.st.Tracking().DeleteViewsBefore(bg, before); err != nil {
		c.log.Printf("error deleting campaign views: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("public.errorProcessingRequest"))
	}

	return nil
}

// DeleteCampaignLinkClicks deletes campaign views older than a given date.
func (c *Core) DeleteCampaignLinkClicks(before time.Time) error {
	if _, err := c.st.Tracking().DeleteClicksBefore(bg, before); err != nil {
		c.log.Printf("error deleting campaign link clicks: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError, c.i18n.Ts("public.errorProcessingRequest"))
	}

	return nil
}
