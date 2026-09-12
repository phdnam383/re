package ruleengine

import (
	"context"
	"errors"

	"re/internal/analysis"
)

var ErrRCARuleNotFound = errors.New("missing rca_rule")

type RuleRepository interface {
	LoadEnabled(ctx context.Context) ([]analysis.RuleDefinition, error)
}
