package rulemanagement

import (
	"context"
	"time"
)

type Repository interface {
	Create(context.Context, Rule) (Rule, error)
	Get(context.Context, string) (Rule, error)
	List(context.Context, ListFilter) (RulePage, error)
	// Update must reject a write if the stored timestamp differs from expected.
	Update(ctx context.Context, rule Rule, expected time.Time) (Rule, error)
	Delete(context.Context, string) error
}
