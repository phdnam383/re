package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// LinkProvider fetches link rows for directed subtree pairs.
type LinkProvider struct {
	db *sql.DB
}

func NewLinkProvider(db *sql.DB) *LinkProvider {
	return &LinkProvider{db: db}
}

// A subtree match on both endpoints. Direction is still part of the lookup, so
// a profile naming A->B gets edges from A's subtree into B's or nothing — the
// provider never substitutes B->A, and it never walks from a matched row to its
// neighbours. What a request may see is still exactly what a profile wrote
// down; what changed is that a profile writes down VDUs, so the set of instance
// pairs it covers is whatever exists at request time rather than whatever
// existed when the profile was written.
const selectLinksSQL = `
SELECT src_path::text,
       dst_path::text,
       COALESCE(src_ip, ''),
       COALESCE(src_port, 0),
       COALESCE(dst_ip, ''),
       COALESCE(dst_port, 0),
       COALESCE(protocol, ''),
       COALESCE(status, ''),
       created_at,
       updated_at
FROM link
WHERE %s
ORDER BY src_path, dst_path`

// maxPairsPerQuery keeps a chunk within maxParamsPerQuery: each pair binds two
// parameters.
const maxPairsPerQuery = maxParamsPerQuery / 2

// FetchLinks returns the link rows for the requested targets. A target that
// matched no row is a MissingContext — for a link that is the interesting
// answer, since a declared connection that is absent from the topology is
// itself a finding.
//
// One row is enough to satisfy a target, however many instance pairs the target
// covers. "Some of the expected edges are missing" is not something the builder
// can state: it does not know how many instances should be connected, and the
// rules that reason over the set are the ones equipped to say what a partial
// set means.
func (p *LinkProvider) FetchLinks(ctx context.Context, targets []contextbuilder.LinkTarget) (contextbuilder.LinkResult, error) {
	if len(targets) == 0 {
		return contextbuilder.LinkResult{}, nil
	}

	var result contextbuilder.LinkResult
	found := make([]bool, len(targets))

	for _, batch := range chunk(targets, maxPairsPerQuery) {
		links, err := p.queryLinks(ctx, batch)
		if err != nil {
			return contextbuilder.LinkResult{}, err
		}
		// A row can satisfy more than one target when two of them overlap, so
		// every target is tested against every row rather than the row being
		// attributed to one of them.
		for _, l := range links {
			for i, t := range targets {
				if !found[i] && t.Matches(l.SrcPath, l.DstPath) {
					found[i] = true
				}
			}
		}
		result.Links = append(result.Links, links...)
	}

	// Overlapping targets can also fetch one edge twice. Two identical rows in
	// a snapshot would make a link count for two in anything that counts them,
	// which is exactly what the quantified link facts do.
	result.Links = dedupeLinks(result.Links)

	for i, t := range targets {
		if !found[i] {
			result.Missing = append(result.Missing, analysis.MissingContext{
				Provider: analysis.ProviderLink,
				Entity:   t.Entity(),
				Reason:   analysis.ReasonNotFound,
			})
		}
	}
	return result, nil
}

// dedupeLinks drops repeated (src, dst) rows, keeping the first. The input is
// already ordered by src then dst within each batch, so this preserves that
// order.
func dedupeLinks(links []analysis.Link) []analysis.Link {
	if len(links) < 2 {
		return links
	}
	type pair struct{ src, dst string }
	seen := make(map[pair]bool, len(links))
	out := links[:0]
	for _, l := range links {
		k := pair{l.SrcPath, l.DstPath}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

func (p *LinkProvider) queryLinks(ctx context.Context, targets []contextbuilder.LinkTarget) ([]analysis.Link, error) {
	args := make([]any, 0, len(targets)*2)
	for _, t := range targets {
		args = append(args, t.SrcPath, t.DstPath)
	}
	query := fmt.Sprintf(selectLinksSQL, ltreeSubtreePairPredicates(1, len(targets)))

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query link: %w", err)
	}
	defer rows.Close()

	var out []analysis.Link
	for rows.Next() {
		var l analysis.Link
		if err := rows.Scan(&l.SrcPath, &l.DstPath, &l.SrcIP, &l.SrcPort,
			&l.DstIP, &l.DstPort, &l.Protocol, &l.Status,
			&l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		l.CreatedAt = l.CreatedAt.UTC()
		l.UpdatedAt = l.UpdatedAt.UTC()
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query link: %w", err)
	}
	return out, nil
}

// compile-time check that the concrete providers satisfy the ports.
var (
	_ contextbuilder.ProfileRepository = (*ProfileRepository)(nil)
	_ contextbuilder.VDUProvider       = (*VDUProvider)(nil)
	_ contextbuilder.LinkProvider      = (*LinkProvider)(nil)
)
