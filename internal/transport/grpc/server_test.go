package grpc

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

type analyzerFunc func(context.Context, analysis.ContextInput) (analysis.AnalysisResult, error)

func (f analyzerFunc) AnalyzeAlert(ctx context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
	return f(ctx, in)
}

func dial(t *testing.T, analyzer Analyzer) mdafv1.RuleEngineClient {
	t.Helper()
	adapter, err := NewServer(analyzer, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(LoggingInterceptor(nil)))
	adapter.Register(server)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		server.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve: %v", err)
		}
	})
	return mdafv1.NewRuleEngineClient(conn)
}

func validRequest() *mdafv1.AnalyzeAlertRequest {
	return &mdafv1.AnalyzeAlertRequest{
		RequestId: "req-1",
		Alert:     &mdafv1.Alert{Id: "a-1", SourcePath: "ims.vdu_a.vnfc_a_1"},
	}
}

func TestServerAnalyzeAlert(t *testing.T) {
	client := dial(t, analyzerFunc(func(_ context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
		if err := in.Validate(); err != nil {
			return analysis.AnalysisResult{}, err
		}
		return analysis.AnalysisResult{
			RequestID: in.RequestID, OverallStatus: analysis.RCAStatusNoConclusion,
			ContextStatus: analysis.StatusComplete, RCAStatus: analysis.RCAStatusNoConclusion,
		}, nil
	}))
	got, err := client.AnalyzeAlert(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRequestId() != "req-1" || got.GetStatus().GetOverall() != analysis.RCAStatusNoConclusion {
		t.Errorf("response = %+v", got)
	}
}

func TestServerRejectsMissingAlert(t *testing.T) {
	client := dial(t, analyzerFunc(func(_ context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
		return analysis.AnalysisResult{}, in.Validate()
	}))
	_, err := client.AnalyzeAlert(context.Background(), &mdafv1.AnalyzeAlertRequest{RequestId: "req-1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s, err = %v", status.Code(err), err)
	}
}
