package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- fakes ---------------------------------------------------------------

// recordingBuilder records what it was asked for and answers from a script.
type recordingBuilder struct {
	calls    int
	gotInput ContextInput

	snapshot ContextSnapshot
	err      error
}

func (b *recordingBuilder) Build(_ context.Context, in ContextInput) (ContextSnapshot, error) {
	b.calls++
	b.gotInput = in
	return b.snapshot, b.err
}

type recordingAnalyzer struct {
	calls       int
	gotSnapshot ContextSnapshot

	result RCAResult
	err    error
}

func (a *recordingAnalyzer) Analyze(_ context.Context, snap ContextSnapshot) (RCAResult, error) {
	a.calls++
	a.gotSnapshot = snap
	return a.result, a.err
}

func validInput() ContextInput {
	return ContextInput{
		RequestID: "req-1",
		Incident:  "inc-1",
		Alerts: []Alert{{
			ID:            "alert-1",
			SourcePath:    "ims.vdu_a.vnfc_a_1",
			ProbableCause: "LINK_TO_PEER_SIPGW_DOWN",
		}},
	}
}

func newTestService(t *testing.T, b ContextBuilder, a RCAAnalyzer) *Service {
	t.Helper()
	s, err := NewService(ServiceOptions{Context: b, RCA: a})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

// --- construction --------------------------------------------------------

func TestNewServiceRequiresBothStages(t *testing.T) {
	if _, err := NewService(ServiceOptions{RCA: &recordingAnalyzer{}}); err == nil {
		t.Error("NewService without a builder = nil, want an error")
	}
	if _, err := NewService(ServiceOptions{Context: &recordingBuilder{}}); err == nil {
		t.Error("NewService without an analyzer = nil, want an error")
	}
}

// --- validation ----------------------------------------------------------

func TestValidationRejectsIncompleteRequests(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(in *ContextInput)
		wantErr string
	}{
		{
			name:    "missing request id",
			mutate:  func(in *ContextInput) { in.RequestID = "" },
			wantErr: "request_id",
		},
		{
			name:    "missing incident",
			mutate:  func(in *ContextInput) { in.Incident = "" },
			wantErr: "incident",
		},
		{
			name:    "no alerts",
			mutate:  func(in *ContextInput) { in.Alerts = nil },
			wantErr: "alerts",
		},
		{
			name:    "empty alert list",
			mutate:  func(in *ContextInput) { in.Alerts = []Alert{} },
			wantErr: "alerts",
		},
		{
			name:    "alert without id",
			mutate:  func(in *ContextInput) { in.Alerts[0].ID = "" },
			wantErr: "alerts[0].id",
		},
		{
			name:    "alert without source path",
			mutate:  func(in *ContextInput) { in.Alerts[0].SourcePath = "" },
			wantErr: "alerts[0].source_path",
		},
		{
			// A nil element in a protobuf repeated field maps to a zero alert.
			// It must be rejected on its empty id, not crash the stage.
			name: "zero alert from a nil protobuf element",
			mutate: func(in *ContextInput) {
				in.Alerts = append(in.Alerts, Alert{})
			},
			wantErr: "alerts[1].id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := &recordingBuilder{}
			analyzer := &recordingAnalyzer{}
			s := newTestService(t, builder, analyzer)

			in := validInput()
			tc.mutate(&in)

			_, err := s.AnalyzeIncident(context.Background(), in)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, tc.wantErr)
			}
			// A request that cannot be analysed must not reach the database.
			if builder.calls != 0 || analyzer.calls != 0 {
				t.Errorf("stages ran on an invalid request: builder=%d rca=%d", builder.calls, analyzer.calls)
			}
		})
	}
}

func TestValidationAcceptsOptionalAlertFields(t *testing.T) {
	// alert_type, probable_cause, severity, state and additional_information
	// are all selector inputs. A selector that matches nothing is a legitimate
	// answer, so rejecting an alert for lacking them would move the operator's
	// profile definitions into the transport.
	builder := &recordingBuilder{snapshot: completeSnapshot()}
	analyzer := &recordingAnalyzer{result: noConclusionResult()}
	s := newTestService(t, builder, analyzer)

	in := ContextInput{
		RequestID: "req-1",
		Incident:  "inc-1",
		Alerts:    []Alert{{ID: "alert-1", SourcePath: "ims.a"}},
	}

	if _, err := s.AnalyzeIncident(context.Background(), in); err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
}

func TestValidationDoesNotNormaliseInput(t *testing.T) {
	// Selector matching compares values. An engine that trimmed or case-folded
	// its input would answer a question the caller did not ask.
	builder := &recordingBuilder{snapshot: completeSnapshot()}
	s := newTestService(t, builder, &recordingAnalyzer{result: noConclusionResult()})

	in := validInput()
	in.Alerts[0].ProbableCause = "  Link_To_Peer_SIPGW_Down  "

	if _, err := s.AnalyzeIncident(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := builder.gotInput.Alerts[0].ProbableCause; got != "  Link_To_Peer_SIPGW_Down  " {
		t.Errorf("probable cause reached the builder as %q; it was modified", got)
	}
}

// --- orchestration -------------------------------------------------------

func TestAnalyzeRunsBothStagesOnceInOrder(t *testing.T) {
	snapshot := completeSnapshot()
	snapshot.Profiles = []string{"profile-a"}

	builder := &recordingBuilder{snapshot: snapshot}
	analyzer := &recordingAnalyzer{result: completeResult()}
	s := newTestService(t, builder, analyzer)

	in := validInput()
	got, err := s.AnalyzeIncident(context.Background(), in)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}

	if builder.calls != 1 {
		t.Errorf("builder calls = %d, want 1", builder.calls)
	}
	if analyzer.calls != 1 {
		t.Errorf("analyzer calls = %d, want 1", analyzer.calls)
	}
	if builder.gotInput.RequestID != in.RequestID {
		t.Errorf("builder input = %+v, want the validated request", builder.gotInput)
	}
	// The rules must reason over the snapshot that was just built, not a
	// rebuilt or filtered copy of it.
	if len(analyzer.gotSnapshot.Profiles) != 1 || analyzer.gotSnapshot.Profiles[0] != "profile-a" {
		t.Errorf("analyzer snapshot = %+v, want the builder's own output", analyzer.gotSnapshot)
	}

	if got.RequestID != in.RequestID || got.Incident != in.Incident {
		t.Errorf("result = %+v, want the request echoed back", got)
	}
}

func TestBuildFailureSkipsRCA(t *testing.T) {
	// Facts answer false and zero for absent data, so a rule set handed an
	// empty snapshot would not error — it would conclude nothing and report
	// that as a clean NO_CONCLUSION.
	buildErr := errors.New("topology query failed")
	builder := &recordingBuilder{err: buildErr}
	analyzer := &recordingAnalyzer{}
	s := newTestService(t, builder, analyzer)

	_, err := s.AnalyzeIncident(context.Background(), validInput())
	if !errors.Is(err, buildErr) {
		t.Fatalf("error = %v, want the build failure", err)
	}
	if analyzer.calls != 0 {
		t.Errorf("analyzer ran %d times after a build failure, want 0", analyzer.calls)
	}
}

func TestRCAFailureProducesNoResult(t *testing.T) {
	rcaErr := errors.New("no rule executed successfully")
	s := newTestService(t,
		&recordingBuilder{snapshot: completeSnapshot()},
		&recordingAnalyzer{err: rcaErr, result: RCAResult{Status: RCAStatusFailed}},
	)

	got, err := s.AnalyzeIncident(context.Background(), validInput())
	if !errors.Is(err, rcaErr) {
		t.Fatalf("error = %v, want the rca failure", err)
	}
	// A caller cannot tell a rule set that concluded nothing from one that
	// never finished, so there is no half-successful response.
	if got.OverallStatus != "" || len(got.RootCauses) != 0 {
		t.Errorf("result = %+v, want the zero value", got)
	}
}

func TestCancellationIsCheckedBeforeAnyStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	builder := &recordingBuilder{}
	s := newTestService(t, builder, &recordingAnalyzer{})

	_, err := s.AnalyzeIncident(ctx, validInput())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if builder.calls != 0 {
		t.Errorf("builder ran on a cancelled request")
	}
}

func TestCancellationFromAStageIsPropagated(t *testing.T) {
	// The transport maps context errors to their own gRPC codes, so the
	// wrapping the service adds must keep errors.Is working.
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled},
		{"deadline exceeded", context.DeadlineExceeded},
	} {
		t.Run("build "+tc.name, func(t *testing.T) {
			s := newTestService(t, &recordingBuilder{err: tc.err}, &recordingAnalyzer{})
			_, err := s.AnalyzeIncident(context.Background(), validInput())
			if !errors.Is(err, tc.err) {
				t.Errorf("error = %v, want %v", err, tc.err)
			}
		})
		t.Run("rca "+tc.name, func(t *testing.T) {
			s := newTestService(t,
				&recordingBuilder{snapshot: completeSnapshot()},
				&recordingAnalyzer{err: tc.err},
			)
			_, err := s.AnalyzeIncident(context.Background(), validInput())
			if !errors.Is(err, tc.err) {
				t.Errorf("error = %v, want %v", err, tc.err)
			}
		})
	}
}

// --- result assembly -----------------------------------------------------

func TestPartialSnapshotReachesRCAAndCarriesItsGaps(t *testing.T) {
	// The most common partial context is one where the failing entity is the
	// very one whose configuration API stopped answering.
	snapshot := ContextSnapshot{
		Status: StatusPartial,
		MissingContext: []MissingContext{{
			Provider: ProviderConfiguration,
			Entity:   "ims.vdu_a.vnfc_a_1",
			Key:      "number_of_log_file",
			Reason:   ReasonTimeout,
		}},
	}
	analyzer := &recordingAnalyzer{result: RCAResult{
		Status:     RCAStatusPartial,
		RootCauses: []RootCause{{ID: "rc-1", Category: "C", Summary: "s", Entity: "ims.a", Role: RolePrimary, Confidence: 0.5}},
	}}
	s := newTestService(t, &recordingBuilder{snapshot: snapshot}, analyzer)

	got, err := s.AnalyzeIncident(context.Background(), validInput())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}

	if analyzer.gotSnapshot.Status != StatusPartial {
		t.Errorf("analyzer saw status %q, want PARTIAL", analyzer.gotSnapshot.Status)
	}
	if got.OverallStatus != RCAStatusPartial {
		t.Errorf("overall = %q, want PARTIAL", got.OverallStatus)
	}
	if len(got.MissingContext) != 1 || got.MissingContext[0].Reason != ReasonTimeout {
		t.Errorf("missing context = %+v, want the snapshot's own gap", got.MissingContext)
	}
	if len(got.RootCauses) != 1 {
		t.Errorf("root causes = %d, want 1 — a partial context still concludes", len(got.RootCauses))
	}
}

func TestMissingContextComesOnlyFromTheSnapshot(t *testing.T) {
	// A failed rule is an operator's problem with the rule set, not a gap in
	// the context. Reporting it as missing context would send the caller
	// looking at the wrong system.
	analyzer := &recordingAnalyzer{result: RCAResult{
		Status: RCAStatusPartial,
		RuleExecutions: []RuleExecution{
			{RuleID: "r1", RuleName: "broken", Status: RuleStatusFailed, Error: "compile failed"},
		},
	}}
	s := newTestService(t, &recordingBuilder{snapshot: completeSnapshot()}, analyzer)

	got, err := s.AnalyzeIncident(context.Background(), validInput())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if len(got.MissingContext) != 0 {
		t.Errorf("missing context = %+v, want none", got.MissingContext)
	}
}

func TestNoConclusionIsASuccessfulResult(t *testing.T) {
	s := newTestService(t, &recordingBuilder{snapshot: completeSnapshot()}, &recordingAnalyzer{result: noConclusionResult()})

	got, err := s.AnalyzeIncident(context.Background(), validInput())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if got.OverallStatus != RCAStatusNoConclusion {
		t.Errorf("overall = %q, want NO_CONCLUSION", got.OverallStatus)
	}
	if len(got.RootCauses) != 0 {
		t.Errorf("root causes = %d, want 0", len(got.RootCauses))
	}
}

func TestResultDoesNotAliasTheStageOutputs(t *testing.T) {
	// The response is built from the result; sharing a backing array would let
	// a mapper's append reach back into the snapshot the builder still owns.
	snapshot := completeSnapshot()
	snapshot.MissingContext = []MissingContext{{Provider: ProviderVDU, Entity: "ims.a"}}
	rca := completeResult()

	s := newTestService(t, &recordingBuilder{snapshot: snapshot}, &recordingAnalyzer{result: rca})

	got, err := s.AnalyzeIncident(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}

	got.RootCauses[0].ID = "mutated"
	got.MissingContext[0].Entity = "mutated"

	if rca.RootCauses[0].ID == "mutated" {
		t.Error("mutating the result reached the RCA output")
	}
	if snapshot.MissingContext[0].Entity == "mutated" {
		t.Error("mutating the result reached the snapshot")
	}
}

// --- status matrix -------------------------------------------------------

func TestStatusMatrix(t *testing.T) {
	tests := []struct {
		name           string
		context, rca   string
		wantOverall    string
		wantInternal   bool
		wantErrMessage string
	}{
		{name: "complete over complete", context: StatusComplete, rca: RCAStatusComplete, wantOverall: RCAStatusComplete},
		{name: "no conclusion", context: StatusComplete, rca: RCAStatusNoConclusion, wantOverall: RCAStatusNoConclusion},
		{name: "partial context", context: StatusPartial, rca: RCAStatusPartial, wantOverall: RCAStatusPartial},
		{name: "partial rca over complete context", context: StatusComplete, rca: RCAStatusPartial, wantOverall: RCAStatusPartial},
		{
			// The rules reached a conclusive answer from data known to be
			// incomplete. There is no way to tell from here which of the two
			// statuses is wrong, so neither is shipped.
			name: "partial context with complete rca", context: StatusPartial, rca: RCAStatusComplete,
			wantInternal: true, wantErrMessage: "context is PARTIAL",
		},
		{
			name: "partial context with no conclusion", context: StatusPartial, rca: RCAStatusNoConclusion,
			wantInternal: true, wantErrMessage: "context is PARTIAL",
		},
		{
			// Reaching here means an error was dropped between the engine and
			// this call, and the response would look successful.
			name: "failed rca without an error", context: StatusComplete, rca: RCAStatusFailed,
			wantInternal: true, wantErrMessage: "FAILED without an error",
		},
		{
			name: "unknown context status", context: "WEIRD", rca: RCAStatusComplete,
			wantInternal: true, wantErrMessage: "context status",
		},
		{
			name: "unknown rca status", context: StatusComplete, rca: "WEIRD",
			wantInternal: true, wantErrMessage: "rca status",
		},
		{
			name: "empty context status", context: "", rca: RCAStatusComplete,
			wantInternal: true, wantErrMessage: "context status",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t,
				&recordingBuilder{snapshot: ContextSnapshot{Status: tc.context}},
				&recordingAnalyzer{result: RCAResult{Status: tc.rca}},
			)

			got, err := s.AnalyzeIncident(context.Background(), validInput())

			if tc.wantInternal {
				if !errors.Is(err, ErrInconsistentStatus) {
					t.Fatalf("error = %v, want ErrInconsistentStatus", err)
				}
				if !strings.Contains(err.Error(), tc.wantErrMessage) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErrMessage)
				}
				return
			}

			if err != nil {
				t.Fatalf("AnalyzeIncident: %v", err)
			}
			if got.OverallStatus != tc.wantOverall {
				t.Errorf("overall = %q, want %q", got.OverallStatus, tc.wantOverall)
			}
			if got.ContextStatus != tc.context {
				t.Errorf("context = %q, want %q", got.ContextStatus, tc.context)
			}
			if got.RCAStatus != tc.rca {
				t.Errorf("rca = %q, want %q", got.RCAStatus, tc.rca)
			}
		})
	}
}

// --- shared fixtures -----------------------------------------------------

func completeSnapshot() ContextSnapshot {
	return ContextSnapshot{Status: StatusComplete}
}

func completeResult() RCAResult {
	return RCAResult{
		Status: RCAStatusComplete,
		RootCauses: []RootCause{{
			ID: "rc-1", Category: "C", Summary: "s", Entity: "ims.a",
			Role: RolePrimary, Confidence: 0.5,
		}},
	}
}

func noConclusionResult() RCAResult {
	return RCAResult{Status: RCAStatusNoConclusion}
}
