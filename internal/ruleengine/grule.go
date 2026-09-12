package ruleengine

import (
	"context"
	"fmt"

	"re/internal/analysis"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/engine"
)

const (
	factCtx    = "Ctx"
	factResult = "Result"
)

type GRLRuntime struct {
	cache *ruleCache
}

func NewGRLRuntime() *GRLRuntime {
	return &GRLRuntime{cache: newRuleCache()}
}

var _ Runtime = (*GRLRuntime)(nil)

func (r *GRLRuntime) Prepare(rule analysis.RuleDefinition) (Session, error) {
	c, err := r.cache.get(rule)
	if err != nil {
		return nil, err
	}

	kb, err := c.instance(rule.ID)
	if err != nil {
		return nil, err
	}

	return &grlSession{rule: rule, kb: kb, maxCycle: uint64(c.ruleCount)}, nil
}

type grlSession struct {
	rule analysis.RuleDefinition
	kb   *ast.KnowledgeBase

	maxCycle uint64
}

var _ Session = (*grlSession)(nil)

func (s *grlSession) Run(
	ctx context.Context,
	facts *Facts,
	out *Result,
) error {
	dctx := ast.NewDataContext()
	if err := dctx.Add(factCtx, facts); err != nil {
		return fmt.Errorf("rca_rule %s: bind %s: %w", s.rule.ID, factCtx, err)
	}
	if err := dctx.Add(factResult, out); err != nil {
		return fmt.Errorf("rca_rule %s: bind %s: %w", s.rule.ID, factResult, err)
	}

	out.retract = func() {
		if entry := dctx.GetRuleEntry(); entry != nil {
			s.kb.RetractRule(entry.RuleName)
		}
	}
	out.currentRule = func() string {
		if entry := dctx.GetRuleEntry(); entry != nil {
			return entry.RuleName
		}
		return ""
	}
	defer func() {
		out.retract = nil
		out.currentRule = nil
	}()

	gengine := engine.NewGruleEngine()

	gengine.MaxCycle = s.maxCycle

	gengine.ReturnErrOnFailedRuleEvaluation = true

	if err := gengine.ExecuteWithContext(ctx, dctx, s.kb); err != nil {
		return fmt.Errorf("rca_rule %s (%s): execute: %w", s.rule.ID, s.rule.Name, err)
	}
	return nil
}
