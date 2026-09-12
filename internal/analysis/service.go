package analysis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

type ServiceOptions struct {
	Context ContextBuilder
	RCA     RCAAnalyzer
	Logger *slog.Logger
}

type Service struct {
	context ContextBuilder
	rca     RCAAnalyzer
	log     *slog.Logger
}

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

func (s *Service) AnalyzeAlert(ctx context.Context, in ContextInput) (AnalysisResult, error) {
	if err := ctx.Err(); err != nil {
		return AnalysisResult{}, err
	}
	if err := in.Validate(); err != nil {
		return AnalysisResult{}, err
	}

	snapshot, err := s.context.Build(ctx, in)
	if err != nil {

		return AnalysisResult{}, fmt.Errorf("analysis: build context: %w", err)
	}

	rca, err := s.rca.Analyze(ctx, snapshot)
	if err != nil {

		return AnalysisResult{}, fmt.Errorf("analysis: run rca: %w", err)
	}

	s.logRuleFailures(in, rca)

	return assembleResult(in, snapshot, rca)
}

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
