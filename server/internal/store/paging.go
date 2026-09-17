package store

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

// Paging is keyset-based, never OFFSET: a page is "the next N rows after this
// position", where the position is a (time, id) pair encoded as an opaque
// cursor. Ids are UUIDv7 (domain/ids.go) so (time, id) is a total, stable sort
// key: the id breaks ties inside a single timestamp and never changes. Rows
// are returned NEWEST FIRST — (time DESC, id DESC) — so a client can render a
// feed as it arrives and fetch older pages on demand; the time column is the
// entity's own creation/occurrence time (tasks: created_at, signals:
// occurred_at), chosen per list method.
//
// Every paged list method follows the same shape so entities added later
// (ListSignals in particular) reuse these helpers rather than a copy:
//
//	cur, err := store.DecodeCursor(p.Cursor)      // ErrInvalid cursor → 400
//	limit := p.EffectiveLimit()                   // default 100, max 1000
//	rows := fetch limit+1 rows admitted by cur    // adapter-specific
//	return store.Paginate(rows, limit, keyFn), nil // trims + sets NextCursor
//
// Bootstrap is NOT paged and is bounded by design: it hydrates the small,
// hot working set (tenant, user, clients, projects, tasks). Anything that can
// grow without bound — signals first — is only reachable through a paged list.

// Page size bounds. A zero/negative Limit means DefaultPageLimit; anything
// above MaxPageLimit is clamped, not rejected, so a client cannot force a
// full-table read through a list endpoint.
const (
	DefaultPageLimit = 100
	MaxPageLimit     = 1000
)

// Page is the caller's paging request. Cursor is opaque: it is whatever the
// previous PageResult.NextCursor said, or "" for the first page.
type Page struct {
	Limit  int
	Cursor string
}

// EffectiveLimit applies the default and the cap to Limit.
func (p Page) EffectiveLimit() int {
	switch {
	case p.Limit <= 0:
		return DefaultPageLimit
	case p.Limit > MaxPageLimit:
		return MaxPageLimit
	}
	return p.Limit
}

// PageResult is one page of rows. Items is never nil (so it marshals as []),
// and NextCursor is "" when this was the last page.
type PageResult[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// Cursor is a decoded paging position. A zero Cursor (At.IsZero()) is the
// start of the list and admits every row.
type Cursor struct {
	At time.Time
	ID string
}

// IsZero reports whether the cursor is the start-of-list position.
func (c Cursor) IsZero() bool { return c.At.IsZero() && c.ID == "" }

// Admits reports whether a row keyed (at, id) belongs on or after this cursor
// in newest-first order — i.e. is strictly older than the cursor position. It
// is the in-memory twin of the SQL predicate `(time_col, id) < ($at, $id)`.
func (c Cursor) Admits(at time.Time, id string) bool {
	if c.IsZero() {
		return true
	}
	return KeysetLess(at, id, c.At, c.ID)
}

// KeysetLess orders (aAt, aID) before (bAt, bID) by time then id — the same
// ordering Postgres applies to a `(timestamptz, uuid)` row value, since uuid
// compares bytewise and canonical lowercase hex sorts identically as text.
func KeysetLess(aAt time.Time, aID string, bAt time.Time, bID string) bool {
	if !aAt.Equal(bAt) {
		return aAt.Before(bAt)
	}
	return aID < bID
}

// EncodeCursor renders a (time, id) position as an opaque string. The time is
// carried as Unix nanoseconds so it round-trips exactly whatever precision the
// adapter stores (Postgres keeps microseconds; memory keeps nanoseconds) and
// is independent of the zone the adapter returned it in.
func EncodeCursor(at time.Time, id string) string {
	raw := strconv.FormatInt(at.UnixNano(), 10) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an EncodeCursor string. "" is the zero cursor. Anything
// else that does not parse is a client error (ValidationError on "cursor"), so
// the HTTP layer answers 400 rather than 500.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, domain.Invalid("cursor", "is malformed")
	}
	at, id, ok := strings.Cut(string(raw), "|")
	// The id is bound as `$n::uuid` by the SQL adapter, so it is checked here
	// where both adapters answer the same 400 (not a Postgres cast failure).
	if !ok || !domain.ValidUUID(id) {
		return Cursor{}, domain.Invalid("cursor", "is malformed")
	}
	ns, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return Cursor{}, domain.Invalid("cursor", "is malformed")
	}
	return Cursor{At: time.Unix(0, ns).UTC(), ID: id}, nil
}

// SortNewestFirst orders rows by (time DESC, id DESC) using key to read each
// row's sort key. Adapters that filter in memory call this before Paginate;
// SQL adapters get the same order from ORDER BY time_col DESC, id DESC.
func SortNewestFirst[T any](rows []T, key func(T) (time.Time, string)) {
	sort.SliceStable(rows, func(i, j int) bool {
		iAt, iID := key(rows[i])
		jAt, jID := key(rows[j])
		return KeysetLess(jAt, jID, iAt, iID)
	})
}

// Paginate turns a limit+1 fetch (already in newest-first order) into a page:
// it keeps the first limit rows and, if a spare row proved there is more, sets
// NextCursor to the last kept row's key. The extra row is the cheapest honest
// "has more" signal — no COUNT, no second query. Items is always non-nil.
func Paginate[T any](rows []T, limit int, key func(T) (time.Time, string)) PageResult[T] {
	res := PageResult[T]{Items: []T{}}
	if len(rows) == 0 {
		return res
	}
	if len(rows) > limit {
		rows = rows[:limit]
		at, id := key(rows[len(rows)-1])
		res.NextCursor = EncodeCursor(at, id)
	}
	res.Items = append(res.Items, rows...)
	return res
}

// TaskKey is the paging key for tasks: created_at, id.
func TaskKey(t domain.Task) (time.Time, string) { return t.CreatedAt, t.ID }
