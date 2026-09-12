package rulemanagement

import (
	"re/internal/analysis"
	"time"
)

// Rule includes the execution definition and management metadata.
type Rule struct {
	analysis.RuleDefinition
	Enabled   bool
	CreatedAt time.Time
}

type CreateInput struct {
	Name        string
	Description string
	Content     string
	Salience    int
	Enabled     *bool
}

// Nil fields are left unchanged by Update.
type UpdateInput struct {
	Name        *string
	Description *string
	Content     *string
	Salience    *int
	Enabled     *bool
}

type ListFilter struct {
	Enabled *bool
	Limit   int
	Offset  int
}

type RulePage struct {
	Items []Rule
	Total int64
}
