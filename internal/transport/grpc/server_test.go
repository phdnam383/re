package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"re/gen/mdafv1"
	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/ruleengine"
)

// --- harness -------------------------------------------------------------

// analyzerFunc adapts a function to the Analyzer interface, so each test
// states the outcome it is about instead of building a stub type.
type analyzerFunc func(context.Context, analysis.ContextInput) (analysis.AnalysisResult, error)

func (f analyzerFunc) AnalyzeIncident(ctx context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
	return f(ctx, in)
}

// dial starts a real generated server and client over an in-memory
// connection.
//
// bufconn rather than a stub call: the generated marshalling, the interceptor
// and the status codes all sit between the caller and the service, and a test
// calling the handler directly would exercise none of them.
func dial(t *testing.T, analyzer Analyzer, logs *bytes.Buffer) mdafv1.IncidentAnalysisEngineClient {
	t.Helper()

	if logs == nil {
		logs = &bytes.Buffer{}
	}
	logger := slog.New(slog.NewJSONHandler(logs, nil))

	srv, err := NewServer(analyzer, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(LoggingInterceptor(logger)))
	srv.Register(server)

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		server.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve: %v", err)
		}
	})

	return mdafv1.NewIncidentAnalysisEngineClient(conn)
}

func validRequest() *mdafv1.AnalyzeIncidentRequest {
	return &mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-1",
		Incident:  "inc-1",
		Alerts: []*mdafv1.Alert{{
			Id:            "alert-1",
			SourcePath:    "ims.vdu_a.vnfc_a_1",
			ProbableCause: "LINK_TO_PEER_SIPGW_DOWN",
		}},
	}
}

// answering builds an analyzer that returns one fixed result.
func answering(result analysis.AnalysisResult) Analyzer {
	return analyzerFunc(func(_ context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
		result.RequestID = in.RequestID
		result.Incident = in.Incident
		return result, nil
	})
}

// failing builds an analyzer that always fails, after validating the request
// the way the real service would — so an error-mapping test is not accidentally
// a validation test.
func failing(err error) Analyzer {
	return analyzerFunc(func(_ context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
		if verr := in.Validate(); verr != nil {
			return analysis.AnalysisResult{}, verr
		}
		return analysis.AnalysisResult{}, err
	})
}

// --- happy path ----------------------------------------------------------

func TestServerHappyPath(t *testing.T) {
	client := dial(t, answering(fullResult()), nil)

	got, err := client.AnalyzeIncident(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}

	if got.GetRequestId() != "req-1" || got.GetIncident() != "inc-1" {
		t.Errorf("echo = %q/%q", got.GetRequestId(), got.GetIncident())
	}
	if got.GetStatus().GetOverall() != analysis.RCAStatusPartial {
		t.Errorf("overall = %q", got.GetStatus().GetOverall())
	}
	if len(got.GetRca().GetRootCauses()) != 2 {
		t.Errorf("root causes = %d, want 2", len(got.GetRca().GetRootCauses()))
	}
	if len(got.GetMeta().GetMissingContext()) != 2 {
		t.Errorf("missing context = %d, want 2", len(got.GetMeta().GetMissingContext()))
	}
	// The value survives protobuf marshalling, not just the mapper.
	value := got.GetRca().GetRootCauses()[0].GetActions()[0].GetValue()
	if value.GetStringValue() != "RESTART" {
		t.Errorf("action value = %v", value)
	}
}

func TestServerPassesTheRequestThrough(t *testing.T) {
	var seen analysis.ContextInput
	client := dial(t, analyzerFunc(func(_ context.Context, in analysis.ContextInput) (analysis.AnalysisResult, error) {
		seen = in
		return analysis.AnalysisResult{
			RequestID: in.RequestID, Incident: in.Incident,
			OverallStatus: analysis.RCAStatusNoConclusion,
			ContextStatus: analysis.StatusComplete,
			RCAStatus:     analysis.RCAStatusNoConclusion,
		}, nil
	}), nil)

	if _, err := client.AnalyzeIncident(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if len(seen.Alerts) != 1 || seen.Alerts[0].ID != "alert-1" {
		t.Errorf("service saw %+v", seen)
	}
}

// --- business outcomes are not errors ------------------------------------

func TestBusinessOutcomesReturnOK(t *testing.T) {
	// A caller told PARTIAL or NO_CONCLUSION by a gRPC error would retry
	// something that will not change.
	tests := []struct {
		name                  string
		overall, ctxSt, rcaSt string
	}{
		{"partial", analysis.RCAStatusPartial, analysis.StatusPartial, analysis.RCAStatusPartial},
		{"no conclusion", analysis.RCAStatusNoConclusion, analysis.StatusComplete, analysis.RCAStatusNoConclusion},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := dial(t, answering(analysis.AnalysisResult{
				OverallStatus: tc.overall, ContextStatus: tc.ctxSt, RCAStatus: tc.rcaSt,
			}), nil)

			got, err := client.AnalyzeIncident(context.Background(), validRequest())
			if err != nil {
				t.Fatalf("AnalyzeIncident = %v, want OK", err)
			}
			if status.Code(err) != codes.OK {
				t.Errorf("code = %s, want OK", status.Code(err))
			}
			if got.GetStatus().GetOverall() != tc.overall {
				t.Errorf("overall = %q, want %q", got.GetStatus().GetOverall(), tc.overall)
			}
		})
	}
}

// --- error mapping -------------------------------------------------------

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name:        "invalid request",
			err:         fmt.Errorf("analysis: %w: request_id is empty", analysis.ErrInvalidRequest),
			wantCode:    codes.InvalidArgument,
			wantMessage: "request_id is empty",
		},
		{
			// The engine is configured wrongly, not asked about something
			// absent — and the message is stable so a caller can tell which
			// half of the configuration is missing.
			name:        "no matching context profile",
			err:         fmt.Errorf("analysis: build context: %w", contextbuilder.ErrContextProfileNotFound),
			wantCode:    codes.FailedPrecondition,
			wantMessage: "missing context_profile",
		},
		{
			name:        "no enabled rca rule",
			err:         fmt.Errorf("analysis: run rca: %w", ruleengine.ErrRCARuleNotFound),
			wantCode:    codes.FailedPrecondition,
			wantMessage: "missing rca_rule",
		},
		{
			name:     "canceled",
			err:      fmt.Errorf("analysis: build context: %w", context.Canceled),
			wantCode: codes.Canceled,
		},
		{
			name:     "deadline exceeded",
			err:      fmt.Errorf("analysis: run rca: %w", context.DeadlineExceeded),
			wantCode: codes.DeadlineExceeded,
		},
		{
			name:     "inconsistent status is internal",
			err:      fmt.Errorf("analysis: %w: context is PARTIAL", analysis.ErrInconsistentStatus),
			wantCode: codes.Internal,
		},
		{
			name:     "anything else is internal",
			err:      errors.New("dial tcp 10.0.0.1:5432: connection refused"),
			wantCode: codes.Internal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := dial(t, failing(tc.err), nil)

			_, err := client.AnalyzeIncident(context.Background(), validRequest())
			if err == nil {
				t.Fatal("AnalyzeIncident = nil, want an error")
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("code = %s, want %s", got, tc.wantCode)
			}
			if tc.wantMessage != "" && !strings.Contains(status.Convert(err).Message(), tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", status.Convert(err).Message(), tc.wantMessage)
			}
		})
	}
}

func TestInvalidRequestIsRejectedWithTheFieldName(t *testing.T) {
	// Validation lives in the domain, so the transport reports whatever the
	// service found rather than duplicating the rules.
	client := dial(t, failing(errors.New("unreachable")), nil)

	tests := []struct {
		name  string
		req   *mdafv1.AnalyzeIncidentRequest
		field string
	}{
		{"nil request", nil, "request_id"},
		{"empty request", &mdafv1.AnalyzeIncidentRequest{}, "request_id"},
		{
			"missing incident",
			&mdafv1.AnalyzeIncidentRequest{RequestId: "r"},
			"incident",
		},
		{
			"no alerts",
			&mdafv1.AnalyzeIncidentRequest{RequestId: "r", Incident: "i"},
			"alerts",
		},
		{
			"nil alert element",
			&mdafv1.AnalyzeIncidentRequest{RequestId: "r", Incident: "i", Alerts: []*mdafv1.Alert{nil}},
			"alerts[0].id",
		},
		{
			"alert without source path",
			&mdafv1.AnalyzeIncidentRequest{
				RequestId: "r", Incident: "i",
				Alerts: []*mdafv1.Alert{{Id: "a"}},
			},
			"alerts[0].source_path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.AnalyzeIncident(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code = %s, want InvalidArgument (err=%v)", status.Code(err), err)
			}
			if !strings.Contains(status.Convert(err).Message(), tc.field) {
				t.Errorf("message = %q, want it to name %q", status.Convert(err).Message(), tc.field)
			}
		})
	}
}

func TestInternalErrorsDoNotLeakDetail(t *testing.T) {
	// An internal failure's text is assembled from whatever broke — a DSN, a
	// SQL fragment, a URL with credentials, a piece of rule content — and none
	// of it belongs on a wire the caller does not own.
	secret := "postgres://engine:hunter2@db.internal:5432/re?sslmode=disable"
	logs := &bytes.Buffer{}
	client := dial(t, failing(fmt.Errorf("connect to database %s: %w", secret, errors.New("refused"))), logs)

	_, err := client.AnalyzeIncident(context.Background(), validRequest())
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s, want Internal", status.Code(err))
	}

	message := status.Convert(err).Message()
	if message != internalMessage {
		t.Errorf("message = %q, want the generic %q", message, internalMessage)
	}
	for _, leak := range []string{"hunter2", "db.internal", "postgres://"} {
		if strings.Contains(message, leak) {
			t.Errorf("message leaks %q: %s", leak, message)
		}
	}

	// The detail still has to exist somewhere an operator can read it.
	if !strings.Contains(logs.String(), "hunter2") {
		t.Error("the full error was not logged server-side")
	}
}

// --- cancellation --------------------------------------------------------

func TestClientCancellationKeepsItsCode(t *testing.T) {
	release := make(chan struct{})
	client := dial(t, analyzerFunc(func(ctx context.Context, _ analysis.ContextInput) (analysis.AnalysisResult, error) {
		<-release
		return analysis.AnalysisResult{}, ctx.Err()
	}), nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
		close(release)
	}()

	_, err := client.AnalyzeIncident(ctx, validRequest())
	if status.Code(err) != codes.Canceled {
		t.Fatalf("code = %s, want Canceled (err=%v)", status.Code(err), err)
	}
}

func TestClientDeadlineKeepsItsCode(t *testing.T) {
	client := dial(t, analyzerFunc(func(ctx context.Context, _ analysis.ContextInput) (analysis.AnalysisResult, error) {
		<-ctx.Done()
		return analysis.AnalysisResult{}, ctx.Err()
	}), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := client.AnalyzeIncident(ctx, validRequest())
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("code = %s, want DeadlineExceeded (err=%v)", status.Code(err), err)
	}
}

func TestServerDeadlineReachesTheService(t *testing.T) {
	// The RPC context is passed through untouched, so the Configuration
	// Provider and each rule row see the caller's deadline.
	got := make(chan bool, 1)
	client := dial(t, analyzerFunc(func(ctx context.Context, _ analysis.ContextInput) (analysis.AnalysisResult, error) {
		_, ok := ctx.Deadline()
		got <- ok
		return analysis.AnalysisResult{
			OverallStatus: analysis.RCAStatusNoConclusion,
			ContextStatus: analysis.StatusComplete,
			RCAStatus:     analysis.RCAStatusNoConclusion,
		}, nil
	}), nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if _, err := client.AnalyzeIncident(ctx, validRequest()); err != nil {
		t.Fatal(err)
	}
	if !<-got {
		t.Error("the service saw no deadline")
	}
}

// --- mapping failure -----------------------------------------------------

func TestUnrepresentableActionValueBecomesInternal(t *testing.T) {
	// Unreachable through the rule engine, which rejects such values at the
	// sink. The transport must still fail rather than panic and take the
	// process with it.
	client := dial(t, answering(analysis.AnalysisResult{
		OverallStatus: analysis.RCAStatusComplete,
		ContextStatus: analysis.StatusComplete,
		RCAStatus:     analysis.RCAStatusComplete,
		RootCauses: []analysis.RootCause{{
			ID: "rc-1", Actions: []analysis.RecommendedAction{{Code: "BAD", Value: make(chan int)}},
		}},
	}), nil)

	_, err := client.AnalyzeIncident(context.Background(), validRequest())
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s, want Internal", status.Code(err))
	}
}

// --- construction --------------------------------------------------------

func TestNewServerRequiresAnAnalyzer(t *testing.T) {
	if _, err := NewServer(nil, nil); err == nil {
		t.Fatal("NewServer(nil) = nil, want an error")
	}
}

// --- interceptor ---------------------------------------------------------

func TestInterceptorLogsTheCallWithoutTheContent(t *testing.T) {
	logs := &bytes.Buffer{}
	client := dial(t, answering(fullResult()), logs)

	req := validRequest()
	req.Alerts[0].AlertType = "COMMUNICATIONS_ALERT"

	if _, err := client.AnalyzeIncident(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	entry := findLogEntry(t, logs, "rpc completed")
	for _, key := range []string{"method", "request_id", "duration_ms", "code", "overall_status", "context_status", "rca_status"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("log entry has no %q: %v", key, entry)
		}
	}
	if entry["request_id"] != "req-1" {
		t.Errorf("request_id = %v", entry["request_id"])
	}
	if entry["code"] != codes.OK.String() {
		t.Errorf("code = %v, want OK", entry["code"])
	}
	if entry["overall_status"] != analysis.RCAStatusPartial {
		t.Errorf("overall_status = %v", entry["overall_status"])
	}

	// A request log that carried the operator's network detail would turn
	// every log sink into a place that data has to be protected.
	raw := logs.String()
	for _, leak := range []string{"COMMUNICATIONS_ALERT", "ims.vdu_a.vnfc_a_1", "RESTART", "SIPGW_DOWN"} {
		if strings.Contains(raw, leak) {
			t.Errorf("log leaks request or response content %q:\n%s", leak, raw)
		}
	}
}

func TestAFailedCallLogsBothTheOutcomeAndTheCause(t *testing.T) {
	// The two log lines have different jobs, and the split is forced by where
	// the error is converted. The server adapter is the last place holding the
	// original error, so it logs the cause; by the time the interceptor sees
	// the call, the error is already the sanitised status the caller got, so
	// it logs the outcome.
	logs := &bytes.Buffer{}
	client := dial(t, failing(errors.New("sql: no such table rca_rule")), logs)

	if _, err := client.AnalyzeIncident(context.Background(), validRequest()); err == nil {
		t.Fatal("want an error")
	}

	outcome := findLogEntry(t, logs, "rpc failed")
	if outcome["code"] != codes.Internal.String() {
		t.Errorf("code = %v, want Internal", outcome["code"])
	}
	if outcome["request_id"] != "req-1" {
		t.Errorf("request_id = %v", outcome["request_id"])
	}

	cause := findLogEntry(t, logs, "analyze incident failed")
	if msg, _ := cause["error"].(string); !strings.Contains(msg, "no such table") {
		t.Errorf("error = %v, want the full failure", cause["error"])
	}
}

func TestInterceptorDoesNotAlterTheOutcome(t *testing.T) {
	// The interceptor observes; it must not become a place where a response or
	// an error can be rewritten.
	sentinel := errors.New("boom")
	interceptor := LoggingInterceptor(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))

	resp, err := interceptor(context.Background(), validRequest(),
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"},
		func(context.Context, any) (any, error) { return "payload", sentinel },
	)
	if resp != "payload" {
		t.Errorf("response = %v, want it untouched", resp)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it untouched", err)
	}
}

func TestInterceptorHandlesAnUnknownRequestType(t *testing.T) {
	interceptor := LoggingInterceptor(nil)
	_, err := interceptor(context.Background(), "not a request",
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"},
		func(context.Context, any) (any, error) { return nil, nil },
	)
	if err != nil {
		t.Fatalf("interceptor = %v, want nil", err)
	}
}

// findLogEntry returns the first JSON log line whose message matches.
func findLogEntry(t *testing.T, logs *bytes.Buffer, message string) map[string]any {
	t.Helper()

	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		if entry["msg"] == message {
			return entry
		}
	}
	t.Fatalf("no log entry with message %q in:\n%s", message, logs.String())
	return nil
}
