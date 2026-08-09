package ruleengine

import (
	"context"
	"errors"

	"re/internal/analysis"
)

// ErrRCARuleNotFound is returned when the repository answered successfully and
// no rule was enabled.
//
// This is FAILED rather than NO_CONCLUSION. "No rule matched" is an answer
// about the incident; "there are no rules" is an answer about the engine's own
// configuration, and reporting the second as the first would let an empty rule
// table look like a healthy system with nothing to report.
var ErrRCARuleNotFound = errors.New("missing rca_rule")

// RuleRepository loads the rule set. The engine reloads at the start of every
// request rather than caching the row set: an operator who enables a rule
// expects the next incident to use it, and the expensive part — compiling the
// GRL — is cached separately and keyed by content, so a reload that returns
// unchanged rows costs one query.
type RuleRepository interface {
	LoadEnabled(ctx context.Context) ([]analysis.RuleDefinition, error)
}
