package postgres

import (
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/media"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

// Row types: the models are plain Go, so Postgres arrays are scanned into
// these wrappers, which embed the model and override the array columns,
// then copied into the model.

type campaignRow struct {
	models.Campaign
	Tags     pq.StringArray `db:"tags"`
	MediaIDs pq.Int64Array  `db:"media_id"`
}

func (r campaignRow) model() models.Campaign {
	c := r.Campaign
	c.Tags = []string(r.Tags)
	c.MediaIDs = []int64(r.MediaIDs)
	return c
}

func campaignModels(rows []campaignRow) models.Campaigns {
	if rows == nil {
		return nil
	}
	out := make(models.Campaigns, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out
}

type listRow struct {
	models.List
	Tags pq.StringArray `db:"tags"`
}

func (r listRow) model() models.List {
	l := r.List
	l.Tags = []string(r.Tags)
	return l
}

func listModels(rows []listRow) []models.List {
	if rows == nil {
		return nil
	}
	out := make([]models.List, len(rows))
	for i, r := range rows {
		out[i] = r.model()
	}
	return out
}

type subscriptionRow struct {
	models.Subscription
	Tags pq.StringArray `db:"tags"`
}

func (r subscriptionRow) model() models.Subscription {
	s := r.Subscription
	s.Tags = []string(r.Tags)
	return s
}

type userRow struct {
	auth.User
	UserRolePerms pq.StringArray `db:"user_role_permissions"`
}

func (r userRow) model() auth.User {
	u := r.User
	u.UserRolePerms = []string(r.UserRolePerms)
	return u
}

type roleRow struct {
	auth.Role
	Permissions pq.StringArray `db:"permissions"`
}

func (r roleRow) model() auth.Role {
	o := r.Role
	o.Permissions = []string(r.Permissions)
	return o
}

type mediaRow struct {
	media.Media
	Tags pq.StringArray `db:"tags"`
}

func (r mediaRow) model() media.Media {
	m := r.Media
	m.Tags = []string(r.Tags)
	return m
}

// loadSubscriberLists lazy loads the lists of the given subscribers
// (moved from models.Subscribers.LoadLists).
func loadSubscriberLists(subs models.Subscribers, stmt *sqlx.Stmt) error {
	var sl []struct {
		SubscriberID int            `db:"subscriber_id"`
		Lists        types.JSONText `db:"lists"`
	}
	if err := stmt.Select(&sl, pq.Array(subs.GetIDs())); err != nil {
		return err
	}

	if len(subs) != len(sl) {
		return errors.New("campaign stats count does not match")
	}

	for i, s := range sl {
		if s.SubscriberID == subs[i].ID {
			subs[i].Lists = s.Lists
		}
	}
	return nil
}

// loadCampaignStats lazy loads campaign stats onto a list of campaigns
// (moved from models.Campaigns.LoadStats).
func loadCampaignStats(camps models.Campaigns, stmt *sqlx.Stmt) error {
	var meta []models.CampaignMeta
	if err := stmt.Select(&meta, pq.Array(camps.GetIDs())); err != nil {
		return err
	}

	if len(camps) != len(meta) {
		return errors.New("campaign stats count does not match")
	}

	for i, c := range meta {
		if c.CampaignID == camps[i].ID {
			camps[i].Lists = c.Lists
			camps[i].Views = c.Views
			camps[i].Clicks = c.Clicks
			camps[i].Bounces = c.Bounces
			camps[i].Media = c.Media
		}
	}
	return nil
}
