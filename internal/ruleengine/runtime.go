package ruleengine

import (
	"context"
	"time"

	"re/internal/analysis"
)

// Runtime turns one rule document into something executable.
//
// The engine depends on this interface and never on a rule library directly,
// so the GRL runtime in grule.go is replaceable and the runner can be tested
// without compiling any rule source.
//
// Preparing is separated from running because compilation and execution have
// different failure modes. For GRL, Prepare performs the compile lookup and
// clones one knowledge base; Run evaluates that document exactly once over the
// complete context snapshot.
type Runtime interface {
	Prepare(rule analysis.RuleDefinition) (Session, error)
}

// Session is one prepared rule document, run exactly once.
//
// An implementation must respect ctx cancellation, must not mutate the facts,
// and must report an evaluation failure as an error rather than as a rule that
// simply did not match — the two are indistinguishable to a caller reading
// only the output, and only one of them is a working rule set.
type Session interface {
	Run(ctx context.Context, facts *Facts, out *Result) error
}

// --- execution records ---------------------------------------------------

// Every loaded row produces exactly one RuleExecution, including rows that
// were never started. A row that ran and matched nothing and a row that never
// ran are different facts, and collapsing both into an absent record would
// make a truncated analysis look like a rule set with nothing to say.

func completedExecution(rule analysis.RuleDefinition, causes, passes int, latency time.Duration) analysis.RuleExecution {
	return analysis.RuleExecution{
		RuleID:         rule.ID,
		RuleName:       rule.Name,
		Status:         analysis.RuleStatusComplete,
		RootCauseCount: causes,
		Passes:         passes,
		Latency:        latency,
	}
}

func failedExecution(rule analysis.RuleDefinition, err error, passes int, latency time.Duration) analysis.RuleExecution {
	return analysis.RuleExecution{
		RuleID:   rule.ID,
		RuleName: rule.Name,
		Status:   analysis.RuleStatusFailed,
		Error:    err.Error(),
		// Passes is 1 only when the single execution returned successfully.
		// A compile, cancellation or evaluation failure leaves it at 0.
		Passes:  passes,
		Latency: latency,
		// RootCauseCount stays 0: a failed row's output is discarded whole, so
		// it contributed nothing regardless of what it managed to assert
		// before failing.
	}
}

func skippedExecution(rule analysis.RuleDefinition, cause error) analysis.RuleExecution {
	return analysis.RuleExecution{
		RuleID:   rule.ID,
		RuleName: rule.Name,
		Status:   analysis.RuleStatusSkipped,
		Error:    cause.Error(),
	}
}
