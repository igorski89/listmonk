package store

import (
	"context"

	"github.com/knadh/listmonk/models"
)

// BounceAction is what happens to a subscriber once Count bounces of a
// type are reached: "none", "unsubscribe", "blocklist" or "delete".
type BounceAction struct {
	Count  int
	Action string
}

// BounceFilter selects bounces. If IDs is set it selects exactly those
// (then the condition fields are ignored); else, if All is set, the
// condition fields select (zero values = no condition); else nothing is
// selected. Scope always applies.
type BounceFilter struct {
	IDs          []int
	All          bool
	CampaignID   int
	SubscriberID int
	Source       string
	Type         string // "soft", "hard", "complaint"
	Scope        ListScope
}

// BounceStore is bounce records and the actions they trigger.
type BounceStore interface {
	// Record stores a bounce for the subscriber (by UUID, else e-mail) and,
	// atomically, applies action once the subscriber's bounces of that
	// type reach action.Count. Bounces of already blocklisted subscribers,
	// or beyond the threshold, are not stored. ErrNotFound if the
	// subscriber is unknown.
	Record(ctx context.Context, b models.Bounce, action BounceAction) error

	// Query returns one page of bounces and the total, ignoring IDs and
	// All. Sort.Field is one of email, campaign_name, source, type,
	// created_at; ties are broken by ID in the same direction.
	Query(ctx context.Context, f BounceFilter, s Sort, p Page) ([]models.Bounce, int, error)

	// Get returns one bounce within scope, or ErrNotFound.
	Get(ctx context.Context, id int, scope ListScope) (models.Bounce, error)

	// BlocklistSubscribers blocklists the subscribers of the matching
	// bounces and unsubscribes them from all lists.
	BlocklistSubscribers(ctx context.Context, f BounceFilter) error

	// Delete deletes the matching bounces; returns how many.
	Delete(ctx context.Context, f BounceFilter) (int, error)

	// DeleteForSubscriber deletes all bounces of one subscriber.
	DeleteForSubscriber(ctx context.Context, ref SubscriberRef) error
}
