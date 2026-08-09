package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// LinkProvider fetches link rows for exact directed pairs.
type LinkProvider struct {
	db *sql.DB
}

func NewLinkProvider(db *sql.DB) *LinkProvider {
	return &LinkProvider{db: db}
}

// A row-value IN list against the (src_path, dst_path) primary key. Direction
// is part of the lookup, so a profile naming A->B gets A->B or nothing — the
// provider never substitutes B->A, and it never walks from a matched row to
// its neighbours. What a request may see is exactly what a profile wrote down.
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
WHERE (src_path, dst_path) IN (%s)
ORDER BY src_path, dst_path`

// maxPairsPerQuery keeps a chunk within maxParamsPerQuery: each pair binds two
// parameters.
const maxPairsPerQuery = maxParamsPerQuery / 2

// FetchLinks returns the link rows for the requested pairs. A pair with no row
// is a MissingContext — for a link that is the interesting answer, since a
// declared connection that is absent from the topology is itself a finding.
func (p *LinkProvider) FetchLinks(ctx context.Context, targets []contextbuilder.LinkTarget) (contextbuilder.LinkResult, error) {
	if len(targets) == 0 {
		return contextbuilder.LinkResult{}, nil
	}

	var result contextbuilder.LinkResult
	found := make(map[contextbuilder.LinkTarget]bool, len(targets))

	for _, batch := range chunk(targets, maxPairsPerQuery) {
		links, err := p.queryLinks(ctx, batch)
		if err != nil {
			return contextbuilder.LinkResult{}, err
		}
		for _, l := range links {
			found[contextbuilder.LinkTarget{SrcPath: l.SrcPath, DstPath: l.DstPath}] = true
		}
		result.Links = append(result.Links, links...)
	}

	for _, t := range targets {
		if !found[t] {
			result.Missing = append(result.Missing, analysis.MissingContext{
				Provider: analysis.ProviderLink,
				Entity:   t.Entity(),
				Reason:   analysis.ReasonNotFound,
			})
		}
	}
	return result, nil
}

func (p *LinkProvider) queryLinks(ctx context.Context, targets []contextbuilder.LinkTarget) ([]analysis.Link, error) {
	args := make([]any, 0, len(targets)*2)
	for _, t := range targets {
		args = append(args, t.SrcPath, t.DstPath)
	}
	query := fmt.Sprintf(selectLinksSQL, ltreePairPlaceholders(1, len(targets)))

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
