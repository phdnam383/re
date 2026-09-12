package ruleengine

import (
	"context"
	"time"

	"re/internal/analysis"
)

type Runtime interface {
	Prepare(rule analysis.RuleDefinition) (Session, error)
}

type Session interface {
	Run(ctx context.Context, facts *Facts, out *Result) error
}

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

		Passes:  passes,
		Latency: latency,

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
