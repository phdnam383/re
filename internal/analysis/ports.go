package analysis

import "context"

type ContextBuilder interface {
	Build(context.Context, ContextInput) (ContextSnapshot, error)
}

type RCAAnalyzer interface {
	Analyze(context.Context, ContextSnapshot) (RCAResult, error)
}
