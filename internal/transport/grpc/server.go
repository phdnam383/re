package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

type Analyzer interface {
	AnalyzeAlert(context.Context, analysis.ContextInput) (analysis.AnalysisResult, error)
}

type Server struct {
	mdafv1.UnimplementedFaultAnalysisServiceServer

	analyzer Analyzer
	log      *slog.Logger
}

func NewServer(analyzer Analyzer, logger *slog.Logger) (*Server, error) {
	if analyzer == nil {
		return nil, errors.New("transport/grpc: analyzer is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{analyzer: analyzer, log: logger}, nil
}

func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	mdafv1.RegisterFaultAnalysisServiceServer(registrar, s)
}

func (s *Server) AnalyzeAlertByRule(
	ctx context.Context,
	req *mdafv1.AlertRuleAnalysisRequest,
) (*mdafv1.AlertRuleAnalysisResponse, error) {
	result, err := s.analyzer.AnalyzeAlert(ctx, requestFromPB(req))
	if err != nil {
		s.logFailure(ctx, req, err)
		return nil, toStatusError(err)
	}

	resp, err := responseToPB(result)
	if err != nil {
		s.logFailure(ctx, req, err)
		return nil, toStatusError(err)
	}
	return resp, nil
}

func (s *Server) logFailure(ctx context.Context, req *mdafv1.AlertRuleAnalysisRequest, err error) {
	s.log.ErrorContext(ctx, "analyze alert failed",
		"request_id", requestID(req),
		"error", err,
	)
}

func requestID(req *mdafv1.AlertRuleAnalysisRequest) string {
	if req == nil {
		return ""
	}
	return req.GetRequestId()
}
