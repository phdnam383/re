package ruleengine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"re/internal/analysis"
)

// DefaultRuleTimeout bounds one rca_rule row.
//
// It is an application limit, not a transport one: a document that has not
// finished in this long is looping or evaluating something pathological, and
// the remaining rows still deserve their turn within the request's own
// deadline. The request deadline always wins when it is shorter, because a row
// budget cannot buy time the caller is no longer waiting for.
const DefaultRuleTimeout = 800 * time.Millisecond

// runOutcome is what one pass over the rule set produced.
type runOutcome struct {
	causes     []analysis.RootCause
	executions []analysis.RuleExecution

	// err is the request-level cancellation that stopped the pass early, nil
	// when every row was reached. A row that failed on its own is not an error
	// here — that is the whole point of failure isolation.
	err error
}

// runRules executes the rule set sequentially and folds what survives into one
// set of root causes.
//
// Sequential by design. Rows are ordered by salience, which is the operator's
// statement of what should be considered first, and running them concurrently
// would make the merge order — and so which document is reported as the one
// that conflicted — depend on scheduling.
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
		// Check before starting rather than after finishing. A row that cannot
		// complete should not be started at all, and the rows behind it are
		// reported as skipped for a stated reason instead of disappearing.
		if err := ctx.Err(); err != nil {
			for _, rest := range rules[i:] {
				out.executions = append(out.executions, skippedExecution(rest, err))
			}
			out.err = err
			break
		}

		exec, contributed := runOne(ctx, rt, facts, rule, timeout, merged)
		out.executions = append(out.executions, exec)
		if contributed != nil {
			merged = contributed
		}
	}

	out.causes = merged.finalize()
	return out
}

// runOne executes a single row and returns its execution record together with
// the merged set to adopt, or nil when the row's output was discarded.
//
// The merge is attempted against a copy. That is what makes the row atomic: a
// document whose third rule contradicts an earlier document must leave nothing
// behind from its first two, and there is no way to know it conflicts until
// the merge is tried.
func runOne(
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

	sink := NewResult()
	err := execute(rctx, rt, rule, facts, sink)
	if err != nil {
		return failedExecution(rule, err, time.Since(start)), nil
	}
	// Invalid output is a failure of the row even though the runtime returned
	// cleanly: GRL has no error channel, so a rule that stated a confidence of
	// 35 or named a cause it never asserted reports it here.
	if err := sink.Err(); err != nil {
		return failedExecution(rule, err, time.Since(start)), nil
	}

	next := merged.clone()
	if err := next.mergeFrom(sink.causes); err != nil {
		return failedExecution(rule, err, time.Since(start)), nil
	}

	return completedExecution(rule, len(sink.causes.order), time.Since(start)), next
}

// execute isolates the runtime call from a panic.
//
// Grule recovers panics raised while evaluating and executing a rule, but the
// compile and clone paths ahead of that are reached with content loaded from
// the database. A malformed document must fail its own row, never the process.
func execute(
	ctx context.Context,
	rt Runtime,
	rule analysis.RuleDefinition,
	facts *Facts,
	sink *Result,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rca_rule %s (%s): panic: %v", rule.ID, rule.Name, r)
		}
	}()
	return rt.Execute(ctx, rule, facts, sink)
}

// sortRules puts the rule set in execution order: salience descending, then
// name ascending.
//
// The repository already returns rows this way, but the engine sorts again
// rather than trusting it. Ordering is what decides which document asserts a
// root cause first and therefore which one is reported as conflicting, so it
// has to hold for a hand-built rule set in a test exactly as it does for a
// PostgreSQL result.
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
