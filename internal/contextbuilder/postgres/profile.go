package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/contextbuilder"
)

// ProfileRepository loads context profiles from the context_profile table.
type ProfileRepository struct {
	db *sql.DB
}

// NewProfileRepository wraps an open database handle. The caller registers the
// driver; see the package documentation.
func NewProfileRepository(db *sql.DB) *ProfileRepository {
	return &ProfileRepository{db: db}
}

const loadEnabledProfilesSQL = `
SELECT name,
       COALESCE(description, ''),
       selector,
       providers
FROM context_profile
WHERE enabled = TRUE
ORDER BY name`

// LoadEnabled returns every enabled profile, decoded and validated.
//
// A query failure, a malformed JSONB document or an invalid definition all
// fail the whole build. Skipping the bad row would be worse than refusing:
// the engine would analyse a narrower scope than the operator declared and
// report success, and a profile that never fires is indistinguishable from one
// that fired and asked for nothing.
//
// Ordering by name is not cosmetic. It fixes which profile a merge conflict is
// reported against and, with the plan's own sort, keeps a build reproducible
// independent of the order PostgreSQL happens to return rows in.
func (r *ProfileRepository) LoadEnabled(ctx context.Context) ([]contextbuilder.ContextProfile, error) {
	rows, err := r.db.QueryContext(ctx, loadEnabledProfilesSQL)
	if err != nil {
		return nil, fmt.Errorf("load enabled context profiles: %w", err)
	}
	defer rows.Close()

	var out []contextbuilder.ContextProfile
	for rows.Next() {
		var (
			name        string
			description string
			selector    []byte
			providers   []byte
		)
		if err := rows.Scan(&name, &description, &selector, &providers); err != nil {
			return nil, fmt.Errorf("scan context profile: %w", err)
		}

		profile, err := contextbuilder.DecodeProfile(name, description, selector, providers)
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load enabled context profiles: %w", err)
	}
	return out, nil
}
