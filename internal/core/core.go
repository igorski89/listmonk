// package core is the collection of re-usable functions that primarily provides data (DB / CRUD) operations
// to the app. For instance, creating and mutating objects like lists, subscribers etc.
// All such methods return an echo.HTTPError{} (which implements error.error) that can be directly returned
// as a response to HTTP handlers without further processing.
package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/knadh/listmonk/internal/i18n"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/throttle"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
)

// bg is the context for store calls until core's methods take one.
var bg = context.Background()

// Core represents the listmonk core with all shared, global functions.
type Core struct {
	h *Hooks

	consts        Constants
	optinThrottle *throttle.Throttle
	i18n          *i18n.I18n
	st            store.Store
	log           *log.Logger
}

// Constants represents constant config.
type Constants struct {
	SendOptinConfirmation bool
	BounceActions         map[string]struct {
		Count  int
		Action string
	}
	CacheSlowQueries bool
}

// Hooks contains external function hooks that are required by the core package.
type Hooks struct {
	SendOptinConfirmation func(models.Subscriber, []int) (int, error)
}

// Opt contains the controllers required to start the core.
type Opt struct {
	Constants Constants
	I18n      *i18n.I18n
	Store     store.Store
	Log       *log.Logger
}

var (
	ErrNotFound = echo.NewHTTPError(http.StatusNotFound, "not found")
)

var (
	regexpSpaces            = regexp.MustCompile(`[\s]+`)
	campQuerySortFields     = []string{"name", "status", models.FieldCreatedAt, "updated_at", "started_at", "send_at"}
	subQuerySortFields      = []string{"email", "status", "name", models.FieldCreatedAt, "updated_at"}
	userQuerySortFields     = []string{"username", "email", models.FieldCreatedAt, "loggedin_at"}
	roleQuerySortFields     = []string{"name", models.FieldCreatedAt, "updated_at"}
	templateQuerySortFields = []string{"name", "type", models.FieldCreatedAt, "updated_at"}
	listQuerySortFields     = []string{"name", "status", models.FieldCreatedAt, "updated_at", "subscriber_count"}
)

// New returns a new instance of the core.
func New(o *Opt, h *Hooks) *Core {
	return &Core{
		h:             h,
		consts:        o.Constants,
		optinThrottle: throttle.New(optinThrottleTTL, optinThrottleCap),
		i18n:          o.I18n,
		st:            o.Store,
		log:           o.Log,
	}
}

// RefreshMatViews refreshes all cached aggregates (Postgres: the
// materialized views).
func (c *Core) RefreshMatViews(concurrent bool) error {
	return c.st.Stats().Refresh(bg)
}

// pqErrMsg returns a database error's message for admins: with the
// database's extra detail if it has one (Postgres' DETAIL), or the
// message of a rejected SQL expression.
func pqErrMsg(err error) string {
	var ie *store.InvalidQueryError
	if errors.As(err, &ie) {
		return ie.Msg
	}
	var de *store.DBError
	if errors.As(err, &de) && de.Detail != "" {
		return fmt.Sprintf("%s. %s", de.Err, de.Detail)
	}
	return err.Error()
}

// sortOf is makeSearchQuery's sort handling: the field if it's in the
// whitelist, else created_at; desc unless asc is asked for.
func sortOf(orderBy, order string, fields []string) store.Sort {
	if !strSliceContains(orderBy, fields) {
		orderBy = models.FieldCreatedAt
	}
	return store.Sort{Field: orderBy, Desc: order != models.OrderAsc}
}

// scopeOf turns upstream's (getAll, permittedIDs) pair into a ListScope.
func scopeOf(getAll bool, permittedIDs []int) store.ListScope {
	return store.ListScope{All: getAll, IDs: permittedIDs}
}

// makeSearchString normalizes a search string for use in SQL search queries.
func makeSearchString(searchStr string) string {
	if searchStr == "" {
		return ""
	}
	return regexpSpaces.ReplaceAllString(strings.TrimSpace(searchStr), " ")
}

// strSliceContains checks if a string is present in the string slice.
func strSliceContains(str string, sl []string) bool {
	for _, s := range sl {
		if s == str {
			return true
		}
	}

	return false
}

// normalizeTags lowercases tags and replaces whitespace with dashes.
func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = regexpSpaces.ReplaceAllString(strings.ToLower(strings.TrimSpace(t)), "-"); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sanitizeSQLExp does basic sanitisation on arbitrary
// SQL query expressions coming from the frontend.
func sanitizeSQLExp(q string) string {
	if len(q) == 0 {
		return ""
	}
	q = strings.TrimSpace(q)

	// Remove semicolon suffix.
	if q[len(q)-1] == ';' {
		q = q[:len(q)-1]
	}
	return q
}

// strHasLen checks if the given string has a length within min-max.
func strHasLen(str string, min, max int) bool {
	return len(str) >= min && len(str) <= max
}
