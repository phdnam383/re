package postgres

import (
	"strconv"
	"strings"
)

// maxParamsPerQuery bounds one statement's bind parameters well below
// PostgreSQL's 65535 ceiling. A profile names its targets explicitly so a plan
// this large is not expected, but the ceiling is a hard protocol error rather
// than a slow query — chunking makes it unreachable whatever the definitions
// say.
const maxParamsPerQuery = 512

// ltreePlaceholders renders "$n::ltree,$n+1::ltree,…" for an IN list.
//
// The values are bound as text and cast in SQL rather than passed as an
// ltree[] array: array encoding is driver-specific, and this package must
// compile and work against any database/sql driver. The cast matters — without
// it PostgreSQL compares against path::text and cannot use the btree index on
// path.
func ltreePlaceholders(start, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(start + i))
		b.WriteString("::ltree")
	}
	return b.String()
}

// ltreePairPlaceholders renders "($1::ltree,$2::ltree),($3::ltree,$4::ltree),…"
// for a row-value IN list, which is how an exact directed link lookup hits the
// (src_path, dst_path) primary key.
func ltreePairPlaceholders(start, pairs int) string {
	var b strings.Builder
	for i := 0; i < pairs; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("($")
		b.WriteString(strconv.Itoa(start + i*2))
		b.WriteString("::ltree,$")
		b.WriteString(strconv.Itoa(start + i*2 + 1))
		b.WriteString("::ltree)")
	}
	return b.String()
}

// chunk splits a slice into runs of at most n, so one logical query can be
// issued as several round-trips.
func chunk[T any](items []T, n int) [][]T {
	if n <= 0 || len(items) <= n {
		return [][]T{items}
	}
	out := make([][]T, 0, (len(items)+n-1)/n)
	for start := 0; start < len(items); start += n {
		end := min(start+n, len(items))
		out = append(out, items[start:end])
	}
	return out
}
