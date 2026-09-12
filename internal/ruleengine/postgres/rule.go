package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/analysis"
	"re/internal/ruleengine"
)

type RuleRepository struct {
	db *sql.DB
}

func NewRuleRepository(db *sql.DB) *RuleRepository {
	return &RuleRepository{db: db}
}

var _ ruleengine.RuleRepository = (*RuleRepository)(nil)

const loadEnabledRulesSQL = `
SELECT id::text,
       name,
       COALESCE(description, ''),
       rule_content,
       salience,
       updated_at
FROM rca_rule
WHERE enabled = TRUE
ORDER BY salience DESC, name ASC`

func (r *RuleRepository) LoadEnabled(ctx context.Context) ([]analysis.RuleDefinition, error) {
	rows, err := r.db.QueryContext(ctx, loadEnabledRulesSQL)
	if err != nil {
		return nil, fmt.Errorf("load enabled rca rules: %w", err)
	}
	defer rows.Close()

	var out []analysis.RuleDefinition
	for rows.Next() {
		var rule analysis.RuleDefinition
		if err := rows.Scan(
			&rule.ID,
			&rule.Name,
			&rule.Description,
			&rule.Content,
			&rule.Salience,
			&rule.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan rca rule: %w", err)
		}

		rule.UpdatedAt = rule.UpdatedAt.UTC()
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load enabled rca rules: %w", err)
	}
	return out, nil
}
