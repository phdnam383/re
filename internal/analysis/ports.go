package analysis

import "context"

// ContextBuilder gathers everything the rules are allowed to reason over.
//
// The interface lives here, next to the domain types, rather than beside the
// implementation: this package must not import internal/contextbuilder, and
// the service that calls it must not either. *contextbuilder.Builder satisfies
// it directly — there is no adapter, because inventing one would be inventing
// a second place for the signatures to disagree.
type ContextBuilder interface {
	Build(context.Context, ContextInput) (ContextSnapshot, error)
}

// RCAAnalyzer runs the operator's rules over a snapshot.
//
// *ruleengine.Engine satisfies it directly, for the same reason.
type RCAAnalyzer interface {
	Analyze(context.Context, ContextSnapshot) (RCAResult, error)
}
