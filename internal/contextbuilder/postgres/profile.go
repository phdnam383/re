package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/contextbuilder"
)

type ProfileRepository struct {
	db *sql.DB
}

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
