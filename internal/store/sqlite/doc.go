// Package sqlite will be the SQLite implementation of internal/store
// (modernc.org/sqlite: pure Go, no cgo). Not written yet.
//
// Driver facts established by a spike (modernc.org/sqlite v1.63.0,
// SQLite 3.54.1):
//
//   - DSN pragmas apply to every pooled connection:
//     _pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate
//   - Columns declared TIMESTAMP are returned as time.Time. With
//     _time_format=sqlite times are written as "2006-01-02 15:04:05.999999999+00:00";
//     in UTC that text sorts in time order (trailing zeros are trimmed,
//     and '+' sorts before digits and '.').
//   - Expressions (MAX(ts), COALESCE, subqueries) lose the declared type:
//     timestamps come back as strings and scanning them into null.Time
//     fails (loudly). Computed timestamps need explicit parsing.
//   - BOOLEAN is stored as INTEGER and scans into bool.
//   - JSON text comes back as a string: models.JSON, StringIntMap and
//     Headers accept strings; json.RawMessage doesn't, so JSON read into it
//     is selected as CAST(x AS BLOB).
//   - ?N parameters can be reused and given out of order.
//   - CGO_ENABLED=0 builds.
package sqlite
