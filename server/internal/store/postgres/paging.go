package postgres

import (
	"fmt"

	"github.com/cadence/server/internal/store"
)

// keysetWhere renders the SQL half of store.Cursor.Admits: an ` AND (col, id)
// < ($n, $n+1)` clause (or "" for the zero cursor) plus the two bound args,
// with placeholders numbered from next. Pair it with
// `ORDER BY col DESC, id DESC LIMIT limit+1` and store.Paginate. The row-value
// comparison is what lets Postgres walk a (tenant_id, col DESC, id DESC) index
// straight to the page; uuid compares bytewise, matching store.KeysetLess.
//
// ListSignals will call this with "occurred_at" — no new SQL to write.
func keysetWhere(cur store.Cursor, col string, next int) (string, []any) {
	if cur.IsZero() {
		return "", nil
	}
	clause := fmt.Sprintf(" AND (%s, id) < ($%d::timestamptz, $%d::uuid)", col, next, next+1)
	return clause, []any{cur.At, cur.ID}
}
