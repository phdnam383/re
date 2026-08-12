package postgres

import (
	"strings"
	"testing"

	"re/internal/analysis"
)

// These run without a database: they cover the two pieces of the link provider
// that decide what the query says and what the snapshot ends up holding.

func TestLtreeSubtreePairPredicates(t *testing.T) {
	tests := []struct {
		name  string
		pairs int
		want  string
	}{
		{
			name: "one target", pairs: 1,
			want: "(src_path <@ $1::ltree AND dst_path <@ $2::ltree)",
		},
		{
			name: "several targets are ORed", pairs: 3,
			want: "(src_path <@ $1::ltree AND dst_path <@ $2::ltree)" +
				" OR (src_path <@ $3::ltree AND dst_path <@ $4::ltree)" +
				" OR (src_path <@ $5::ltree AND dst_path <@ $6::ltree)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ltreeSubtreePairPredicates(1, tc.pairs); got != tc.want {
				t.Errorf("= %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestLtreeSubtreePairPredicatesBindsTwoParametersPerTarget(t *testing.T) {
	// The chunk size is derived from this ratio, so a change here that nobody
	// noticed would let a large plan exceed the parameter ceiling.
	got := ltreeSubtreePairPredicates(1, maxPairsPerQuery)
	if n := strings.Count(got, "$"); n != maxPairsPerQuery*2 {
		t.Errorf("parameters = %d, want %d", n, maxPairsPerQuery*2)
	}
	if maxPairsPerQuery*2 > maxParamsPerQuery {
		t.Errorf("a full chunk binds %d parameters, over the %d ceiling",
			maxPairsPerQuery*2, maxParamsPerQuery)
	}
}

func TestDedupeLinks(t *testing.T) {
	link := func(src, dst, status string) analysis.Link {
		return analysis.Link{SrcPath: src, DstPath: dst, Status: status}
	}

	in := []analysis.Link{
		link("ims.a.vnfc_1", "ims.b.vnfc_1", "DOWN"),
		link("ims.a.vnfc_2", "ims.b.vnfc_1", "UP"),
		// The same edge again, fetched by a second overlapping target.
		link("ims.a.vnfc_1", "ims.b.vnfc_1", "DOWN"),
		// The reverse edge is a different link and must survive.
		link("ims.b.vnfc_1", "ims.a.vnfc_1", "UP"),
	}

	got := dedupeLinks(in)
	if len(got) != 3 {
		t.Fatalf("links = %d, want 3: %+v", len(got), got)
	}
	want := [][2]string{
		{"ims.a.vnfc_1", "ims.b.vnfc_1"},
		{"ims.a.vnfc_2", "ims.b.vnfc_1"},
		{"ims.b.vnfc_1", "ims.a.vnfc_1"},
	}
	for i, w := range want {
		if got[i].SrcPath != w[0] || got[i].DstPath != w[1] {
			t.Errorf("link %d = %s -> %s, want %s -> %s", i, got[i].SrcPath, got[i].DstPath, w[0], w[1])
		}
	}
}

func TestDedupeLinksLeavesShortInputAlone(t *testing.T) {
	if got := dedupeLinks(nil); got != nil {
		t.Errorf("= %+v, want nil", got)
	}
	one := []analysis.Link{{SrcPath: "ims.a", DstPath: "ims.b"}}
	if got := dedupeLinks(one); len(got) != 1 {
		t.Errorf("= %+v, want the single link", got)
	}
}
