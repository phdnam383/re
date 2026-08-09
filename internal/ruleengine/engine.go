package ruleengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"re/internal/analysis"
)

// Options configures an Engine.
type Options struct {
	// Rules is where the rule set comes from. Required.
	Rules RuleRepository

	// Runtime executes the rule documents. Defaults to the GRL runtime, which
	// is what every stored rule is written in; it is injectable so the runner
	// can be exercised without compiling rule source.
	Runtime Runtime

	// RuleTimeout bounds one row. Defaults to DefaultRuleTimeout.
	RuleTimeout time.Duration

	// Logger is optional. Rule failures are already carried in the result's
	// execution records; this is for an operator watching a running process.
	Logger *slog.Logger
}

// Engine analyses a context snapshot against the operator's rule set.
type Engine struct {
	rules   RuleRepository
	runtime Runtime
	timeout time.Duration
	log     *slog.Logger
}

// New builds an Engine.
func New(opts Options) (*Engine, error) {
	if opts.Rules == nil {
		return nil, errors.New("ruleengine: rule repository is required")
	}

	e := &Engine{
		rules:   opts.Rules,
		runtime: opts.Runtime,
		timeout: opts.RuleTimeout,
		log:     opts.Logger,
	}
	if e.runtime == nil {
		e.runtime = NewGRLRuntime()
	}
	if e.timeout <= 0 {
		e.timeout = DefaultRuleTimeout
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	return e, nil
}

// Analyze runs the enabled rules over the snapshot.
//
// The rule set is loaded here rather than passed in: the caller has no way to
// know which rules are enabled, and an engine that accepted a rule list would
// let a stale one be replayed. Loading costs one query per request; the
// expensive half — compiling the GRL — is cached by content, so an unchanged
// rule set recompiles nothing.
//
// A partial context is analysed in full. Facts answer false and zero for data
// that was not obtained, so rules reason over what is known and the result is
// reported PARTIAL — refusing to run would be worse, since the most common
// partial context is one where the failing entity is exactly the one whose
// configuration API stopped answering.
func (e *Engine) Analyze(ctx context.Context, snap analysis.ContextSnapshot) (analysis.RCAResult, error) {
	if err := ctx.Err(); err != nil {
		return analysis.RCAResult{Status: analysis.RCAStatusFailed}, err
	}

	rules, err := e.rules.LoadEnabled(ctx)
	if err != nil {
		return analysis.RCAResult{Status: analysis.RCAStatusFailed},
			fmt.Errorf("ruleengine: load enabled rules: %w", err)
	}
	if len(rules) == 0 {
		return analysis.RCAResult{Status: analysis.RCAStatusFailed}, ErrRCARuleNotFound
	}

	outcome := runRules(ctx, e.runtime, NewFacts(snap), sortRules(rules), e.timeout)

	result := analysis.RCAResult{
		RootCauses:     outcome.causes,
		RuleExecutions: outcome.executions,
	}

	status, statusErr := deriveStatus(snap.Status, outcome)
	result.Status = status

	e.logExecutions(outcome)

	// A cancellation is reported even when some rows did finish. The caller
	// asked for an analysis of the whole rule set and did not get one, and the
	// status alone cannot say whether the missing rows would have concluded
	// anything.
	if outcome.err != nil {
		return result, outcome.err
	}
	return result, statusErr
}

// deriveStatus turns the run into the single status the response carries.
//
// The distinctions it draws are the ones an operator acts on:
//
//   - FAILED means the engine did not analyse anything, so the absence of root
//     causes says nothing about the incident.
//   - NO_CONCLUSION means the engine analysed everything and the rule set had
//     nothing to say. That is a statement about the rule set.
//   - PARTIAL means the answer was reached with less than the full picture —
//     missing context, a failed row, or a run cut short — so root causes may be
//     present and are not the whole story.
//   - COMPLETE means every rule ran over a complete context.
func deriveStatus(contextStatus string, outcome runOutcome) (string, error) {
	var completed, failed, skipped int
	for _, ex := range outcome.executions {
		switch ex.Status {
		case analysis.RuleStatusComplete:
			completed++
		case analysis.RuleStatusFailed:
			failed++
		case analysis.RuleStatusSkipped:
			skipped++
		}
	}

	if completed == 0 {
		return analysis.RCAStatusFailed,
			fmt.Errorf("ruleengine: no rule executed successfully (%d failed, %d skipped)", failed, skipped)
	}

	degraded := contextStatus != analysis.StatusComplete || failed > 0 || skipped > 0
	switch {
	case degraded:
		return analysis.RCAStatusPartial, nil
	case len(outcome.causes) > 0:
		return analysis.RCAStatusComplete, nil
	default:
		return analysis.RCAStatusNoConclusion, nil
	}
}

func (e *Engine) logExecutions(outcome runOutcome) {
	for _, ex := range outcome.executions {
		if ex.Status == analysis.RuleStatusComplete {
			continue
		}
		e.log.Warn("rca rule did not complete",
			"rule_id", ex.RuleID,
			"rule_name", ex.RuleName,
			"status", ex.Status,
			"error", ex.Error,
		)
	}
}
