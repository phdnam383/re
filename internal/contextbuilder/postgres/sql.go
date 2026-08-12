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

// ltreeSubtreePairPredicates renders
// "(src_path <@ $1::ltree AND dst_path <@ $2::ltree) OR (…)" — one disjunct per
// link target.
//
// <@ rather than equality because a target names subtree roots: a VDU pair has
// to match every instance pair beneath it, and an instance pair still matches
// only itself since a path is a descendant of itself.
//
// This trades the (src_path, dst_path) primary-key lookup for a scan of the
// link table. The table holds one row per directed edge in the topology, which
// is small and bounded by the deployment; if it ever stops being, the index to
// add is GIST on src_path and dst_path.
func ltreeSubtreePairPredicates(start, pairs int) string {
	var b strings.Builder
	for i := 0; i < pairs; i++ {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(src_path <@ $")
		b.WriteString(strconv.Itoa(start + i*2))
		b.WriteString("::ltree AND dst_path <@ $")
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
