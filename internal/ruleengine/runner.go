package ruleengine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"re/internal/analysis"
)

const DefaultRuleTimeout = 800 * time.Millisecond

type runOutcome struct {
	causes     []analysis.RootCause
	executions []analysis.RuleExecution

	err error
}

func runRules(
	ctx context.Context,
	rt Runtime,
	facts *Facts,
	rules []analysis.RuleDefinition,
	timeout time.Duration,
) runOutcome {
	if timeout <= 0 {
		timeout = DefaultRuleTimeout
	}

	merged := newCauseSet()
	out := runOutcome{executions: make([]analysis.RuleExecution, 0, len(rules))}

	for i, rule := range rules {

		if err := ctx.Err(); err != nil {
			for _, rest := range rules[i:] {
				out.executions = append(out.executions, skippedExecution(rest, err))
			}
			out.err = err
			break
		}

		exec, contributed := runRule(ctx, rt, facts, rule, timeout, merged)
		out.executions = append(out.executions, exec)
		if contributed != nil {
			merged = contributed
		}
	}

	out.causes = merged.finalize()
	return out
}

func runRule(
	ctx context.Context,
	rt Runtime,
	facts *Facts,
	rule analysis.RuleDefinition,
	timeout time.Duration,
	merged *causeSet,
) (analysis.RuleExecution, *causeSet) {
	start := time.Now()

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := prepare(rt, rule)
	if err != nil {
		return failedExecution(rule, err, 0, time.Since(start)), nil
	}

	sink := NewResult()
	if err := rctx.Err(); err != nil {
		return failedExecution(rule, err, 0, time.Since(start)), nil
	}

	if err := run(rctx, session, facts, sink); err != nil {
		return failedExecution(rule, err, 0, time.Since(start)), nil
	}

	if err := sink.Err(); err != nil {
		return failedExecution(rule, err, 1, time.Since(start)), nil
	}

	next := merged.clone()
	if err := next.mergeFrom(sink.causes); err != nil {
		return failedExecution(rule, err, 1, time.Since(start)), nil
	}

	return completedExecution(rule, len(sink.causes.order), 1, time.Since(start)), next
}

func prepare(rt Runtime, rule analysis.RuleDefinition) (s Session, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("rca_rule %s (%s): panic: %v", rule.ID, rule.Name, r)
		}
	}()
	return rt.Prepare(rule)
}

func run(
	ctx context.Context,
	session Session,
	facts *Facts,
	sink *Result,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rule execution panic: %v", r)
		}
	}()
	return session.Run(ctx, facts, sink)
}

func sortRules(rules []analysis.RuleDefinition) []analysis.RuleDefinition {
	out := append([]analysis.RuleDefinition(nil), rules...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Salience != out[j].Salience {
			return out[i].Salience > out[j].Salience
		}
		return out[i].Name < out[j].Name
	})
	return out
}
