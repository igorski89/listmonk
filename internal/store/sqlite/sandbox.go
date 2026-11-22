package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/internal/store"
	msqlite "modernc.org/sqlite"
)

// The sandbox runs statements that contain a user's raw SQL expression
// (SubscriberFilter.Expr, the subscribers:sql_query feature). Postgres
// relies on a read-only transaction plus an EXPLAIN table check; here
// SQLite's authorizer vets every table, column and function at prepare
// time, on a read-only, query_only connection:
//
//   - only SELECT; any write, PRAGMA, ATTACH, DDL or transaction control
//     is denied;
//   - reads only from the tables a subscriber filter may use (as upstream's
//     allowed list) and json_each/json_tree;
//   - only the functions below; no recursive CTEs;
//   - a time limit (sandboxTimeout), so one runaway expression can't hold
//     the sandbox connections.

var sandboxTables = map[string]bool{
	"subscribers":      true,
	"lists":            true,
	"subscriber_lists": true,
	"campaigns":        true,
	"campaign_lists":   true,
	"campaign_views":   true,
	"links":            true,
	"link_clicks":      true,
	"bounces":          true,
	"json_each":        true,
	"json_tree":        true,
}

var sandboxFuncs = map[string]bool{}

// sandboxTimeout bounds one sandboxed statement (a page of subscribers,
// a count, an export batch).
const sandboxTimeout = 60 * time.Second

func init() {
	for _, f := range strings.Fields(`
		abs avg char coalesce concat concat_ws count date datetime format glob hex ifnull iif instr
		julianday length like lower ltrim max min nullif octet_length printf replace round rtrim
		sign strftime substr substring sum time timediff total trim typeof unicode unixepoch upper
		json json_array json_array_length json_extract json_insert json_object json_quote json_type
		json_valid json_group_array json_group_object -> ->>`) {
		sandboxFuncs[f] = true
	}
}

func sandboxPolicy(action msqlite.AuthorizerActionCode, arg1, arg2, _, _ string) msqlite.AuthorizerReturnCode {
	switch action {
	case msqlite.AuthSelect:
		return msqlite.AuthorizerOK
	case msqlite.AuthRead:
		if sandboxTables[strings.ToLower(arg1)] {
			return msqlite.AuthorizerOK
		}
	case msqlite.AuthFunction:
		if sandboxFuncs[strings.ToLower(arg2)] {
			return msqlite.AuthorizerOK
		}
	}
	return msqlite.AuthorizerDeny
}

// sandboxed runs fn on a read-only connection with the sandbox policy.
// Statements the policy rejects fail at prepare time; they come back as
// *store.InvalidQueryError.
func (s *Store) sandboxed(ctx context.Context, fn func(*sqlx.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, sandboxTimeout)
	defer cancel()

	conn, err := s.sb.Connx(ctx)
	if err != nil {
		return wrap(err)
	}
	defer conn.Close()

	if err := conn.Raw(func(dc any) error {
		reg, ok := dc.(msqlite.AuthorizerRegisterer)
		if !ok {
			return fmt.Errorf("sqlite: driver has no authorizer")
		}
		return reg.RegisterAuthorizer(sandboxPolicy)
	}); err != nil {
		return err
	}
	defer conn.Raw(func(dc any) error {
		return dc.(msqlite.AuthorizerRegisterer).RegisterAuthorizer(nil)
	})

	if err := fn(conn); err != nil {
		return &store.InvalidQueryError{Msg: err.Error(), Err: err}
	}
	return nil
}

// checkExpr rejects expressions that try to end the statement or add
// another one. (The authorizer would deny whatever followed, but SQLite
// would also silently ignore a trailing statement in a query, so reject
// it outright, as Postgres' prepare does.)
func checkExpr(expr string) error {
	inStr := false
	for _, c := range expr {
		switch {
		case c == '\'':
			inStr = !inStr
		case c == ';' && !inStr:
			return &store.InvalidQueryError{Msg: "multiple statements are not allowed"}
		}
	}
	return nil
}
