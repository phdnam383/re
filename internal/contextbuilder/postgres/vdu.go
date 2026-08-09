package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// VDUProvider fetches VDU rows by exact path, each with every VNFC beneath it.
type VDUProvider struct {
	db *sql.DB
}

func NewVDUProvider(db *sql.DB) *VDUProvider {
	return &VDUProvider{db: db}
}

// Every column of the table, not a projection: the builder cannot know which
// attribute a rule will read, and a rule needing one more column should not
// require a change here. Deliberately no join to managed_object or
// vnfc_binding — those carry tree bookkeeping and slot history, which is not
// something a rule reasons over.
const selectVDUsSQL = `
SELECT path::text,
       name,
       type,
       namespace,
       workload,
       replicas,
       COALESCE(selector, ''),
       nf_config,
       updated_at
FROM vdu
WHERE path IN (%s)
ORDER BY path`

// The VNFC lookup is a subtree query, not a parent_path equality: <@ matches
// every descendant, so a VDU yields its VNFCs however deep the tree convention
// later places them. Joining vdu is what supplies vdu_path, which is not a
// column on vnfc.
const selectVNFCsSQL = `
SELECT parent.path::text,
       child.path::text,
       COALESCE(child.k8s_uid, ''),
       child.name,
       child.status,
       child.instance_config,
       child.created_at,
       child.updated_at
FROM vdu parent
JOIN vnfc child ON child.path <@ parent.path
WHERE parent.path IN (%s)
ORDER BY parent.path, child.path`

// FetchVDUs returns the requested VDUs and their VNFCs.
//
// A path with no vdu row is a MissingContext. A VDU that exists with no VNFC
// at all is not: a workload scaled to zero is a fact, and reporting it as a
// gap would make "nothing is running" indistinguishable from "we could not
// find out".
func (p *VDUProvider) FetchVDUs(ctx context.Context, paths []string) (contextbuilder.VDUResult, error) {
	if len(paths) == 0 {
		return contextbuilder.VDUResult{}, nil
	}

	var result contextbuilder.VDUResult
	found := make(map[string]bool, len(paths))

	for _, batch := range chunk(paths, maxParamsPerQuery) {
		vdus, err := p.queryVDUs(ctx, batch)
		if err != nil {
			return contextbuilder.VDUResult{}, err
		}
		for _, v := range vdus {
			found[v.Path] = true
		}
		result.VDUs = append(result.VDUs, vdus...)

		vnfcs, err := p.queryVNFCs(ctx, batch)
		if err != nil {
			return contextbuilder.VDUResult{}, err
		}
		result.VNFCs = append(result.VNFCs, vnfcs...)
	}

	// Reported in request order, which the plan already sorted.
	for _, path := range paths {
		if !found[path] {
			result.Missing = append(result.Missing, analysis.MissingContext{
				Provider: analysis.ProviderVDU,
				Entity:   path,
				Reason:   analysis.ReasonNotFound,
			})
		}
	}
	return result, nil
}

func (p *VDUProvider) queryVDUs(ctx context.Context, paths []string) ([]analysis.VDU, error) {
	query := fmt.Sprintf(selectVDUsSQL, ltreePlaceholders(1, len(paths)))
	rows, err := p.db.QueryContext(ctx, query, toArgs(paths)...)
	if err != nil {
		return nil, fmt.Errorf("query vdu: %w", err)
	}
	defer rows.Close()

	var out []analysis.VDU
	for rows.Next() {
		var (
			v        analysis.VDU
			nfConfig []byte
		)
		if err := rows.Scan(&v.Path, &v.Name, &v.Type, &v.Namespace, &v.Workload,
			&v.Replicas, &v.Selector, &nfConfig, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan vdu: %w", err)
		}
		if v.NFConfig, err = decodeJSONObject(nfConfig); err != nil {
			return nil, fmt.Errorf("vdu %s: nf_config: %w", v.Path, err)
		}
		v.UpdatedAt = v.UpdatedAt.UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query vdu: %w", err)
	}
	return out, nil
}

func (p *VDUProvider) queryVNFCs(ctx context.Context, paths []string) ([]analysis.VNFC, error) {
	query := fmt.Sprintf(selectVNFCsSQL, ltreePlaceholders(1, len(paths)))
	rows, err := p.db.QueryContext(ctx, query, toArgs(paths)...)
	if err != nil {
		return nil, fmt.Errorf("query vnfc: %w", err)
	}
	defer rows.Close()

	var out []analysis.VNFC
	for rows.Next() {
		var (
			c              analysis.VNFC
			instanceConfig []byte
		)
		if err := rows.Scan(&c.VDUPath, &c.Path, &c.K8sUID, &c.Name, &c.Status,
			&instanceConfig, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan vnfc: %w", err)
		}
		if c.InstanceConfig, err = decodeJSONObject(instanceConfig); err != nil {
			return nil, fmt.Errorf("vnfc %s: instance_config: %w", c.Path, err)
		}
		c.CreatedAt = c.CreatedAt.UTC()
		c.UpdatedAt = c.UpdatedAt.UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query vnfc: %w", err)
	}
	return out, nil
}

// decodeJSONObject decodes a nullable jsonb column. NULL and an empty document
// both yield a nil map.
//
// A document that is valid JSON but not an object fails the provider rather
// than being dropped: the column is documented as an object, and silently
// discarding configuration would let a rule read a zero value as if the NF had
// been asked.
func decodeJSONObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// toArgs widens a []string for the variadic query arguments.
func toArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
