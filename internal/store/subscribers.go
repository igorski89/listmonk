package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/knadh/listmonk/models"
)

// SubscriberRef addresses one subscriber by exactly one of ID, UUID or
// Email (the first non-zero one wins, in that order).
type SubscriberRef struct {
	ID    int
	UUID  string
	Email string
}

// ListScope limits a caller to the lists it has permission for.
// All=true ignores IDs. The zero value permits no lists (fails closed);
// callers with full permission pass ListScope{All: true}.
type ListScope struct {
	All bool
	IDs []int
}

// SubscriberFilter selects subscribers. Zero values mean "no condition".
// It replaces upstream's positional ($1 listIDs, $2 subStatus, $3 search,
// …) arguments and the %query% template substitution, which become each
// backend's business.
type SubscriberFilter struct {
	// IDs restricts to these subscribers (export of a selection).
	IDs []int

	// Search is the free-text search box (email, name).
	Search string

	// Expr is an optional raw SQL boolean expression over the subscriber
	// tables, IN THE BACKEND'S DIALECT (see Store.Dialect). Callers must
	// have checked the subscribers:sql_query permission. Each backend
	// sandboxes it and returns *InvalidQueryError when it rejects it:
	//   - Postgres: read-only transaction + EXPLAIN table whitelist, as
	//     upstream does today.
	//   - SQLite: a read-only connection with an authorizer
	//     (modernc.org/sqlite >= 1.63) that allows only SELECT, reads of
	//     the whitelisted tables and a whitelist of functions.
	// Bulk writes "by filter" first resolve Expr to subscriber IDs
	// read-only, then write by ID; Expr never reaches a writable statement.
	// A dialect-neutral filter builder will later add Expr-free conditions
	// to this struct.
	Expr string

	// ListIDs: subscribers on any of these lists. Empty = all.
	ListIDs []int

	// SubscriptionStatus filters the subscriber_lists status on ListIDs
	// ("unconfirmed", "confirmed", "unsubscribed"), or "none" for
	// subscribers on no list at all. Empty = any.
	SubscriptionStatus string

	// Status filters the subscriber's own status ("enabled",
	// "blocklisted"). Empty = any.
	Status string
}

// SubscriberInput is the editable part of a subscriber. On Update, empty
// fields (and nil Attribs) keep the stored value.
type SubscriberInput struct {
	Email   string
	Name    string
	Status  string // empty = "enabled" on insert
	Attribs models.JSON
}

// ListChange describes the subscriptions to add or keep alongside an
// insert or update. Lists may be given by ID, UUID or both.
type ListChange struct {
	ListIDs   []int
	ListUUIDs []string

	// Status for the given lists: "unconfirmed" or "confirmed"
	// (preconfirmed). A blocklisted subscriber's subscriptions are always
	// "unsubscribed".
	Status string

	// ReplaceExisting (update only) deletes the subscriber's other
	// subscriptions, within Permitted.
	ReplaceExisting bool

	// Permitted limits which existing subscriptions ReplaceExisting may
	// delete (and, from upstream v7.0.0, which existing subscribers an
	// import may modify).
	Permitted ListScope

	// AllowResubscribe (update only): existing non-confirmed subscriptions
	// get Status (used by the public subscription form). Without it they
	// keep their status. Confirmed ones always stay confirmed.
	AllowResubscribe bool
}

// ImportMode chooses what an import batch does.
type ImportMode string

const (
	ImportSubscribe ImportMode = "subscribe" // upsert and subscribe to lists
	ImportBlocklist ImportMode = "blocklist" // upsert as blocklisted, unsubscribe from all lists
)

// ImportOptions apply to a whole import batch.
type ImportOptions struct {
	Mode               ImportMode
	ListIDs            []int
	SubscriptionStatus string    // for new subscriptions
	OverwriteUserInfo  bool      // overwrite name/attribs of existing subscribers
	OverwriteSubStatus bool      // overwrite the status of existing subscriptions
	Permitted          ListScope // existing subscribers this import may modify (upstream v7.0.0+)
}

// ActivityKind is a subscriber's tracking data that can be deleted.
type ActivityKind string

const (
	ActivityCampaignViews ActivityKind = "campaign_views"
	ActivityLinkClicks    ActivityKind = "link_clicks"
)

// SubscriberStore is subscribers, their profile data and bulk operations.
// Every returned models.Subscriber has its Lists loaded.
type SubscriberStore interface {
	// Get returns one subscriber, or ErrNotFound.
	Get(ctx context.Context, ref SubscriberRef) (models.Subscriber, error)

	// GetByEmails returns the subscribers with these e-mails (unknown
	// ones are skipped; an empty result is not an error).
	GetByEmails(ctx context.Context, emails []string) (models.Subscribers, error)

	// Query returns one page of the filtered subscribers and the total
	// number of matches. Sort.Field is one of email, status, name,
	// created_at, updated_at; empty = id.
	Query(ctx context.Context, f SubscriberFilter, s Sort, p Page) (models.Subscribers, int, error)

	// Export validates f once and returns a batch reader: each call
	// returns up to limit matching subscribers with id > afterID, in id
	// order (keyset pagination for streaming large exports).
	Export(ctx context.Context, f SubscriberFilter) (func(afterID, limit int) ([]models.SubscriberExport, error), error)

	// HasLists reports, per subscriber ID, whether it is on any of listIDs.
	HasLists(ctx context.Context, subIDs, listIDs []int) (map[int]bool, error)

	// Lists returns the lists a subscriber is on, narrowed by the given
	// lists (IDs or UUIDs; both empty = all), subscription status and list
	// type ("public"/"private"; empty = any).
	Lists(ctx context.Context, ref SubscriberRef, listIDs []int, listUUIDs []string, subStatus, listType string) ([]models.List, error)

	// Profile returns everything stored about a subscriber (data export
	// for the subscriber themselves). Private list names outside scope
	// are masked.
	Profile(ctx context.Context, ref SubscriberRef, scope ListScope) (models.SubscriberExportProfile, error)

	// Activity returns a subscriber's campaign views and link clicks.
	Activity(ctx context.Context, id int, scope ListScope) (models.SubscriberActivity, error)

	// Charts returns a subscriber's views and clicks over time as JSON
	// ready for the frontend.
	Charts(ctx context.Context, id int, scope ListScope) (json.RawMessage, error)

	// DeleteActivity deletes one kind of a subscriber's tracking data.
	DeleteActivity(ctx context.Context, id int, kind ActivityKind) error

	// Insert creates a subscriber (UUID generated by the store) with the
	// given subscriptions and returns it. Lists by ID, or by UUID if no
	// IDs are given. A duplicate e-mail returns
	// *ConflictError{Field: "email"}.
	Insert(ctx context.Context, in SubscriberInput, lists ListChange) (models.Subscriber, error)

	// Update changes a subscriber's fields and returns it; ErrNotFound if
	// it does not exist, *ConflictError{Field: "email"} on a duplicate.
	Update(ctx context.Context, id int, in SubscriberInput) (models.Subscriber, error)

	// UpdateWithLists is Update plus a subscription change, atomically.
	UpdateWithLists(ctx context.Context, id int, in SubscriberInput, lists ListChange) (models.Subscriber, error)

	// Import upserts one batch of subscribers atomically: all rows or
	// none (the importer stops at the first failed batch, as today).
	// Existing subscribers are matched by e-mail. In subscribe mode an
	// existing subscriber with no subscription to a list in o.Permitted is
	// left unchanged and its e-mail returned in skipped (upstream: import
	// can't update subscribers of lists the user can't manage). Updating
	// the lists' updated_at after an import belongs to the list store.
	Import(ctx context.Context, rows []SubscriberInput, o ImportOptions) (skipped []string, err error)

	// Blocklist blocklists subscribers and marks all their subscriptions
	// unsubscribed. The ...ByFilter methods here and in
	// SubscriptionStore return how many subscribers the filter matched.
	Blocklist(ctx context.Context, ids []int) error
	BlocklistByFilter(ctx context.Context, f SubscriberFilter) (int, error)

	// Delete deletes subscribers by ID and/or UUID.
	Delete(ctx context.Context, ids []int, uuids []string) error
	DeleteByFilter(ctx context.Context, f SubscriberFilter) (int, error)

	// Maintenance: delete subscribers on no list / blocklisted ones.
	DeleteOrphans(ctx context.Context) (int, error)
	DeleteBlocklisted(ctx context.Context) (int, error)

	// UnsubscribeByCampaign handles the unsubscribe link in a campaign
	// e-mail. blocklist: blocklist the subscriber and unsubscribe from all
	// lists. Else allUnconfirmed: unsubscribe from every unconfirmed
	// subscription. Else: unsubscribe from the campaign's lists. Unknown
	// UUIDs are a no-op.
	UnsubscribeByCampaign(ctx context.Context, subUUID, campUUID string, blocklist, allUnconfirmed bool) error
}

// SubscriptionStore is the subscriber <-> list relation.
type SubscriptionStore interface {
	// List returns the lists a subscriber has a subscription to (any
	// status, unsubscribed included); with allLists, every list, the
	// subscription fields null where there is none.
	List(ctx context.Context, ref SubscriberRef, allLists bool) ([]models.Subscription, error)

	// Add subscribes subscribers to lists. With a status, new and
	// existing subscriptions get it; with status "", new ones are
	// "unconfirmed" and existing ones keep theirs. AddByFilter only adds
	// new subscriptions (existing ones are left alone, whatever the
	// status); f selects the subscribers (f.ListIDs = source lists),
	// listIDs are the target lists.
	Add(ctx context.Context, subIDs, listIDs []int, status string) error
	AddByFilter(ctx context.Context, f SubscriberFilter, listIDs []int, status string) (int, error)

	// Remove deletes subscriptions.
	Remove(ctx context.Context, subIDs, listIDs []int) error
	RemoveByFilter(ctx context.Context, f SubscriberFilter, listIDs []int) (int, error)

	// Unsubscribe marks subscriptions unsubscribed. Lists by ID or UUID.
	Unsubscribe(ctx context.Context, subIDs, listIDs []int, listUUIDs []string) error
	UnsubscribeByFilter(ctx context.Context, f SubscriberFilter, listIDs []int) (int, error)

	// ConfirmOptin confirms a subscriber's subscriptions to the given
	// lists, merging meta (IP, time) into their meta. Only "unconfirmed"
	// ones change: an old opt-in link can't resubscribe (upstream v7.0.0).
	ConfirmOptin(ctx context.Context, subUUID string, listUUIDs []string, meta models.JSON) error

	// DeleteUnconfirmed deletes unconfirmed subscriptions to double opt-in
	// lists created before t.
	DeleteUnconfirmed(ctx context.Context, before time.Time) (int, error)
}
