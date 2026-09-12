package ruleengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"re/internal/analysis"
)

type Options struct {

	Rules RuleRepository

	Runtime Runtime

	RuleTimeout time.Duration

	Logger *slog.Logger
}

type Engine struct {
	rules   RuleRepository
	runtime Runtime
	timeout time.Duration
	log     *slog.Logger
}

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

	if outcome.err != nil {
		return result, outcome.err
	}
	return result, statusErr
}

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
