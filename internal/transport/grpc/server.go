package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

// Analyzer is the only thing this package needs from the application.
//
// Declared here rather than imported as *analysis.Service so the transport
// depends on a method it calls instead of on a struct it does not own — which
// is also what lets every test in this package drive the server without a
// database.
type Analyzer interface {
	AnalyzeAlert(context.Context, analysis.ContextInput) (analysis.AnalysisResult, error)
}

// Server adapts the Analysis Service to the generated service interface.
type Server struct {
	mdafv1.UnimplementedRuleEngineServer

	analyzer Analyzer
	log      *slog.Logger
}

// NewServer builds the adapter.
func NewServer(analyzer Analyzer, logger *slog.Logger) (*Server, error) {
	if analyzer == nil {
		return nil, errors.New("transport/grpc: analyzer is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{analyzer: analyzer, log: logger}, nil
}

// Register attaches the service to a gRPC server.
//
// It takes a grpc.ServiceRegistrar so the composition root never names the
// generated register function — the one place the protobuf package would
// otherwise leak past this package.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	mdafv1.RegisterRuleEngineServer(registrar, s)
}

// AnalyzeAlert maps the request, runs the analysis and maps the response.
//
// The RPC context is passed through untouched. A caller's deadline is the only
// budget the engine honours at this level, and adding one here would cut short
// work the caller was still waiting for.
func (s *Server) AnalyzeAlert(
	ctx context.Context,
	req *mdafv1.AnalyzeAlertRequest,
) (*mdafv1.AnalyzeAlertResponse, error) {
	result, err := s.analyzer.AnalyzeAlert(ctx, requestFromPB(req))
	if err != nil {
		s.logFailure(ctx, req, err)
		return nil, toStatusError(err)
	}

	// PARTIAL and NO_CONCLUSION are answers, not failures: the first says the
	// engine reached a conclusion without the full picture, the second that
	// the rule set had nothing to say. A caller told either one by a gRPC
	// error would have to retry something that will not change.
	resp, err := responseToPB(result)
	if err != nil {
		s.logFailure(ctx, req, err)
		return nil, toStatusError(err)
	}
	return resp, nil
}

// logFailure records the full error server-side.
//
// This is the counterpart to the deliberately vague Internal message: the
// detail has to exist somewhere, and the server's own log is where it can be
// read without being handed to whoever called.
func (s *Server) logFailure(ctx context.Context, req *mdafv1.AnalyzeAlertRequest, err error) {
	s.log.ErrorContext(ctx, "analyze alert failed",
		"request_id", requestID(req),
		"error", err,
	)
}

func requestID(req *mdafv1.AnalyzeAlertRequest) string {
	if req == nil {
		return ""
	}
	return req.GetRequestId()
}
