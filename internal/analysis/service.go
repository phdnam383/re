package analysis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ServiceOptions configures a Service.
type ServiceOptions struct {
	// Context and RCA are both required.
	Context ContextBuilder
	RCA     RCAAnalyzer

	// Logger is optional and defaults to a discarding logger. It records the
	// rule-level detail that never leaves the process — a failed rule row is
	// an operator's problem with the rule set, and the response deliberately
	// carries no trace of it.
	Logger *slog.Logger
}

// Service runs one alert through both stages and assembles the result.
//
// It owns the order and nothing else: no retries, no caching, no timeout of
// its own. The caller's deadline reaches both stages unchanged, because each
// already bounds the work it controls — the Configuration Provider bounds one
// HTTP GET, the rule engine bounds one rca_rule row — and a third budget layered
// on top could only cut those short for reasons neither stage can see.
type Service struct {
	context ContextBuilder
	rca     RCAAnalyzer
	log     *slog.Logger
}

// NewService builds a Service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Context == nil {
		return nil, errors.New("analysis: context builder is required")
	}
	if opts.RCA == nil {
		return nil, errors.New("analysis: rca analyzer is required")
	}
	s := &Service{context: opts.Context, rca: opts.RCA, log: opts.Logger}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s, nil
}

// AnalyzeAlert builds the context, runs the rules over it and assembles the
// answer.
//
// Neither stage is repeated and neither reloads the other's definitions: the
// builder reads context_profile at its start and the engine reads rca_rule at
// its start, each taking whatever is current when it runs. They are
// deliberately not wrapped in one transaction — an operator who enables a
// profile and a rule expects the next alert to use both, and a snapshot
// isolating them from that would be a cache with extra steps.
func (s *Service) AnalyzeAlert(ctx context.Context, in ContextInput) (AnalysisResult, error) {
	if err := ctx.Err(); err != nil {
		return AnalysisResult{}, err
	}
	if err := in.Validate(); err != nil {
		return AnalysisResult{}, err
	}

	snapshot, err := s.context.Build(ctx, in)
	if err != nil {
		// The rules are not run against a context that could not be built.
		// Facts answer false and zero for absent data, so a rule set handed an
		// empty snapshot would not error — it would conclude nothing, and
		// report that as a clean NO_CONCLUSION.
		return AnalysisResult{}, fmt.Errorf("analysis: build context: %w", err)
	}

	// A PARTIAL snapshot goes to the rules exactly as it is. The most common
	// partial context is one where the failing entity is the very one whose
	// configuration API stopped answering, so refusing to analyse it would
	// withhold the answer precisely when it is needed.
	rca, err := s.rca.Analyze(ctx, snapshot)
	if err != nil {
		// No partially successful response. A caller cannot tell a rule set
		// that concluded nothing from one that never finished, so a failed
		// analysis is an error and not an empty result.
		return AnalysisResult{}, fmt.Errorf("analysis: run rca: %w", err)
	}

	s.logRuleFailures(in, rca)

	return assembleResult(in, snapshot, rca)
}

// assembleResult builds the public result from the two stage outputs.
func assembleResult(in ContextInput, snapshot ContextSnapshot, rca RCAResult) (AnalysisResult, error) {
	overall, err := deriveOverallStatus(snapshot.Status, rca.Status)
	if err != nil {
		return AnalysisResult{}, fmt.Errorf("analysis: %w", err)
	}

	return AnalysisResult{
		RequestID:     in.RequestID,
		Incident:      in.Incident,
		OverallStatus: overall,
		ContextStatus: snapshot.Status,
		RCAStatus:     rca.Status,

		// Copied rather than aliased. The response is built from this and the
		// snapshot belongs to the stage that produced it; sharing the backing
		// array would let a mapper's append reach back into it.
		RootCauses:     cloneRootCauses(rca.RootCauses),
		MissingContext: append([]MissingContext(nil), snapshot.MissingContext...),
	}, nil
}

func cloneRootCauses(in []RootCause) []RootCause {
	out := make([]RootCause, len(in))
	for i, cause := range in {
		out[i] = cause
		out[i].Components = make([]Component, len(cause.Components))
		for j, component := range cause.Components {
			out[i].Components[j] = component
			if component.Action != nil {
				action := *component.Action
				out[i].Components[j].Action = &action
			}
		}
	}
	return out
}

// logRuleFailures records the rule rows that did not complete.
//
// This is the only place the execution trace is used. It stays in the log
// because it describes the engine's own rule set rather than the alert, and
// a caller cannot act on it.
func (s *Service) logRuleFailures(in ContextInput, rca RCAResult) {
	for _, ex := range rca.RuleExecutions {
		if ex.Status == RuleStatusComplete {
			continue
		}
		s.log.Warn("rca rule did not complete",
			"request_id", in.RequestID,
			"rule_id", ex.RuleID,
			"rule_name", ex.RuleName,
			"status", ex.Status,
			"error", ex.Error,
		)
	}
}
