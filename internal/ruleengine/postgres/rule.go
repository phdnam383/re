package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"re/internal/analysis"
	"re/internal/ruleengine"
)

// RuleRepository loads RCA rules from the rca_rule table.
type RuleRepository struct {
	db *sql.DB
}

// NewRuleRepository wraps an open database handle. The caller registers the
// driver; see the package documentation.
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

// LoadEnabled returns every enabled rule in execution order.
//
// A query failure fails the whole analysis before any rule runs. There is no
// partial rule set: analysing an incident against an unknown subset of the
// operator's rules and reporting the outcome as an answer would be worse than
// reporting that the rules could not be read.
//
// Rows are ordered here as well as in the engine. Salience is the operator's
// statement of what to consider first, and the tie-break on name is what keeps
// two rules of equal salience from swapping places between runs — PostgreSQL
// is under no obligation to return them in a stable order otherwise.
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
		// The column is timestamptz; normalising to UTC here keeps a rule set
		// comparable regardless of the session time zone the connection
		// happens to carry.
		rule.UpdatedAt = rule.UpdatedAt.UTC()
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load enabled rca rules: %w", err)
	}
	return out, nil
}
