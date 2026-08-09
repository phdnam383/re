package ruleengine

import (
	"context"
	"fmt"

	"re/internal/analysis"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/engine"
)

// Fact names bound into the GRL data context. These are the identifiers rule
// authors write, so they are part of the rule-authoring contract and not an
// implementation detail. Exactly two are bound: everything a rule may read is
// under Ctx, and everything it may write goes through Result.
const (
	factCtx    = "Ctx"
	factResult = "Result"
)

// GRLRuntime executes rule documents written in GRL.
type GRLRuntime struct {
	cache *ruleCache
}

// NewGRLRuntime creates a runtime with an empty compile cache. The cache lives
// on the runtime, so a long-running engine compiles each distinct rule content
// once for the life of the process.
func NewGRLRuntime() *GRLRuntime {
	return &GRLRuntime{cache: newRuleCache()}
}

var _ Runtime = (*GRLRuntime)(nil)

// Execute runs one document to completion.
//
// Grule fires exactly one rule per cycle and then re-evaluates every remaining
// rule, so a rule whose "then" does not falsify its own "when" would be
// selected forever. Result.Assert retracts the firing rule, which is what makes
// a rule single-shot without asking authors to remember it.
//
// Retraction is used rather than DataContext.Complete(): completing ends the
// whole run at the first assertion, and an rca_rule row is a scenario document
// whose later rules must still get their turn.
func (r *GRLRuntime) Execute(
	ctx context.Context,
	rule analysis.RuleDefinition,
	facts *Facts,
	out *Result,
) error {
	c, err := r.cache.get(rule)
	if err != nil {
		return err
	}

	kb, err := c.instance(rule.ID)
	if err != nil {
		return err
	}

	dctx := ast.NewDataContext()
	if err := dctx.Add(factCtx, facts); err != nil {
		return fmt.Errorf("rca_rule %s: bind %s: %w", rule.ID, factCtx, err)
	}
	if err := dctx.Add(factResult, out); err != nil {
		return fmt.Errorf("rca_rule %s: bind %s: %w", rule.ID, factResult, err)
	}

	// The sink learns which rule is speaking from the data context, which
	// Grule points at the rule it is currently executing. Wiring it as a
	// callback keeps the rule library out of result.go.
	out.retract = func() {
		if entry := dctx.GetRuleEntry(); entry != nil {
			kb.RetractRule(entry.RuleName)
		}
	}
	defer func() { out.retract = nil }()

	gengine := engine.NewGruleEngine()

	// One cycle per rule in the document. Every rule that fires is retracted,
	// so a document where all of them match needs exactly that many cycles and
	// then finds nothing runnable. A rule that fires without asserting is never
	// retracted, keeps being selected, and trips this bound — which is the
	// point: it is a rule with a "then" that does nothing, and failing the row
	// says so instead of letting it spin to the library default of 5000.
	gengine.MaxCycle = uint64(c.ruleCount)

	// Surface evaluation errors. Without this a rule whose condition blew up —
	// a nil dereference in a fact call, a type mismatch — is logged and treated
	// as a rule that did not match, which is exactly the reading that must
	// never happen: it turns a broken rule into a silent negative.
	gengine.ReturnErrOnFailedRuleEvaluation = true

	if err := gengine.ExecuteWithContext(ctx, dctx, kb); err != nil {
		return fmt.Errorf("rca_rule %s (%s): execute: %w", rule.ID, rule.Name, err)
	}
	return nil
}
