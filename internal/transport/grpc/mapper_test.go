package grpc

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

// --- request -------------------------------------------------------------

func TestRequestFromPBMapsEveryAlertField(t *testing.T) {
	req := &mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-1",
		Incident:  "inc-1",
		Alerts: []*mdafv1.Alert{{
			Id:                "alert-1",
			SourcePath:        "ims.vdu_a.vnfc_a_1",
			AlertType:         "COMMUNICATIONS_ALERT",
			ProbableCause:     "LINK_TO_PEER_SIPGW_DOWN",
			PerceivedSeverity: "CRITICAL",
			State:             "ACTIVE",
			CreatedAt:         "2026-06-18T00:00:00Z",
		}},
	}

	got := requestFromPB(req)

	if got.RequestID != "req-1" || got.Incident != "inc-1" {
		t.Errorf("request = %+v", got)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(got.Alerts))
	}
	a := got.Alerts[0]
	fields := []struct {
		name      string
		got, want string
	}{
		{"id", a.ID, "alert-1"},
		{"source_path", a.SourcePath, "ims.vdu_a.vnfc_a_1"},
		{"alert_type", a.AlertType, "COMMUNICATIONS_ALERT"},
		{"probable_cause", a.ProbableCause, "LINK_TO_PEER_SIPGW_DOWN"},
		{"perceived_severity", a.PerceivedSeverity, "CRITICAL"},
		{"state", a.State, "ACTIVE"},
		{"created_at", a.CreatedAt, "2026-06-18T00:00:00Z"},
	}
	for _, f := range fields {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
		}
	}
	if a.AdditionalInformation != nil {
		t.Errorf("additional_information = %#v, want nil", a.AdditionalInformation)
	}
}

func TestRequestFromPBKeepsJSONTypes(t *testing.T) {
	// Selector matching compares typed values: 85 and "85" are different, and
	// a selector can match on null. A hand-written conversion would be a
	// second implementation of that mapping, and the first thing it would lose
	// is null.
	info, err := structpb.NewStruct(map[string]any{
		"metric":         "overload_ram",
		"observed_value": 93.5,
		"threshold":      85,
		"enabled":        true,
		"nothing":        nil,
		"nested":         map[string]any{"zone": "zone-a", "port": 5060},
		"list":           []any{"a", 2, false},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := requestFromPB(&mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-1", Incident: "inc-1",
		Alerts: []*mdafv1.Alert{{Id: "a", SourcePath: "ims.a", AdditionalInformation: info}},
	})

	ai := got.Alerts[0].AdditionalInformation
	if ai["metric"] != "overload_ram" {
		t.Errorf("metric = %#v", ai["metric"])
	}
	if v, ok := ai["observed_value"].(float64); !ok || v != 93.5 {
		t.Errorf("observed_value = %#v, want float64(93.5)", ai["observed_value"])
	}
	// protobuf has one number type, so an integer literal arrives as a float.
	// That is the JSON model the selector already compares against.
	if v, ok := ai["threshold"].(float64); !ok || v != 85 {
		t.Errorf("threshold = %#v, want float64(85)", ai["threshold"])
	}
	if v, ok := ai["enabled"].(bool); !ok || !v {
		t.Errorf("enabled = %#v, want true", ai["enabled"])
	}
	// Present and null, which is a different fact from absent.
	if v, present := ai["nothing"]; !present || v != nil {
		t.Errorf("nothing = %#v (present=%v), want a present nil", v, present)
	}
	nested, ok := ai["nested"].(map[string]any)
	if !ok || nested["zone"] != "zone-a" {
		t.Errorf("nested = %#v", ai["nested"])
	}
	list, ok := ai["list"].([]any)
	if !ok || len(list) != 3 || list[0] != "a" {
		t.Errorf("list = %#v", ai["list"])
	}
}

func TestRequestFromPBHandlesAbsentStructures(t *testing.T) {
	tests := []struct {
		name       string
		req        *mdafv1.AnalyzeIncidentRequest
		wantAlerts int
	}{
		{name: "nil request", req: nil},
		{name: "no alerts", req: &mdafv1.AnalyzeIncidentRequest{RequestId: "r", Incident: "i"}},
		{
			name: "empty alert slice",
			req:  &mdafv1.AnalyzeIncidentRequest{RequestId: "r", Incident: "i", Alerts: []*mdafv1.Alert{}},
		},
		{
			// Not valid protobuf, but a hand-rolled client can send it. It
			// becomes a zero alert that fails validation, never a panic.
			name:       "nil alert element",
			req:        &mdafv1.AnalyzeIncidentRequest{RequestId: "r", Incident: "i", Alerts: []*mdafv1.Alert{nil}},
			wantAlerts: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := requestFromPB(tc.req)
			if len(got.Alerts) != tc.wantAlerts {
				t.Errorf("alerts = %d, want %d", len(got.Alerts), tc.wantAlerts)
			}
		})
	}
}

func TestNilAlertReachesValidationAsInvalid(t *testing.T) {
	// The mapper converts and does not judge; the rejection must come from the
	// domain, so a caller arriving by any route is held to the same rule.
	in := requestFromPB(&mdafv1.AnalyzeIncidentRequest{
		RequestId: "r", Incident: "i", Alerts: []*mdafv1.Alert{nil},
	})
	err := in.Validate()
	if !errors.Is(err, analysis.ErrInvalidRequest) {
		t.Fatalf("Validate = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "alerts[0].id") {
		t.Errorf("Validate = %q, want it to name the alert", err)
	}
}

func TestAdditionalInformationAbsenceIsNotAnEmptyObject(t *testing.T) {
	// A selector distinguishes "the key is absent" from "the key is present".
	// An alert with no additional information must not look like one carrying
	// an empty object.
	empty, err := structpb.NewStruct(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		in   *structpb.Struct
	}{
		{"nil struct", nil},
		{"empty struct", empty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := additionalInformationFromPB(tc.in); got != nil {
				t.Errorf("additional information = %#v, want nil", got)
			}
		})
	}
}

func TestAdditionalInformationKeepsEveryKey(t *testing.T) {
	// Presence semantics: a key the caller sent must still be a key the
	// selector can find, whatever its value.
	info, err := structpb.NewStruct(map[string]any{"a": nil, "b": "", "c": false, "d": 0})
	if err != nil {
		t.Fatal(err)
	}

	got := additionalInformationFromPB(info)
	for _, key := range []string{"a", "b", "c", "d"} {
		if _, present := got[key]; !present {
			t.Errorf("key %q disappeared", key)
		}
	}
}

// --- response ------------------------------------------------------------

func fullResult() analysis.AnalysisResult {
	return analysis.AnalysisResult{
		RequestID:     "req-1",
		Incident:      "inc-1",
		OverallStatus: analysis.RCAStatusPartial,
		ContextStatus: analysis.StatusPartial,
		RCAStatus:     analysis.RCAStatusPartial,
		RootCauses: []analysis.RootCause{
			{
				ID: "rc-a", Category: "SIPGW_DOWN", Summary: "first", Entity: "ims.a",
				Role: analysis.RolePrimary, Confidence: 0.55,
				Actions: []analysis.RecommendedAction{
					{Code: "RESTART_VNFC", MOInstance: "ims.a", Op: analysis.OpReplace, Value: "RESTART"},
					{Code: "SET_CONFIG", MOInstance: "ims.a", Op: analysis.OpReplace, Value: 3},
				},
			},
			{
				ID: "rc-b", Category: "OTHER", Summary: "second", Entity: "ims.b",
				Role: analysis.RoleContributing, Confidence: 0.3,
			},
		},
		MissingContext: []analysis.MissingContext{
			{Provider: analysis.ProviderConfiguration, Entity: "ims.a", Key: "k", Reason: analysis.ReasonTimeout},
			{Provider: analysis.ProviderVDU, Entity: "ims.z", Reason: analysis.ReasonNotFound},
		},
	}
}

func TestResponseToPBMapsEverything(t *testing.T) {
	got, err := responseToPB(fullResult())
	if err != nil {
		t.Fatalf("responseToPB: %v", err)
	}

	if got.GetRequestId() != "req-1" || got.GetIncident() != "inc-1" {
		t.Errorf("echo = %q/%q", got.GetRequestId(), got.GetIncident())
	}
	if got.GetStatus().GetOverall() != analysis.RCAStatusPartial ||
		got.GetStatus().GetContext() != analysis.StatusPartial ||
		got.GetStatus().GetRca() != analysis.RCAStatusPartial {
		t.Errorf("status = %+v", got.GetStatus())
	}
	// meta.context_status restates status.context where a caller reading meta
	// expects it; deriving it from anywhere else would let the two disagree.
	if got.GetMeta().GetContextStatus() != got.GetStatus().GetContext() {
		t.Errorf("meta.context_status = %q, status.context = %q",
			got.GetMeta().GetContextStatus(), got.GetStatus().GetContext())
	}

	causes := got.GetRca().GetRootCauses()
	if len(causes) != 2 {
		t.Fatalf("root causes = %d, want 2", len(causes))
	}
	// The rule engine fixed this order; the mapper must not re-sort it.
	if causes[0].GetId() != "rc-a" || causes[1].GetId() != "rc-b" {
		t.Errorf("order = %q, %q", causes[0].GetId(), causes[1].GetId())
	}
	if causes[0].GetCategory() != "SIPGW_DOWN" || causes[0].GetSummary() != "first" ||
		causes[0].GetEntity() != "ims.a" || causes[0].GetRole() != analysis.RolePrimary ||
		causes[0].GetConfidence() != 0.55 {
		t.Errorf("root cause = %+v", causes[0])
	}

	actions := causes[0].GetActions()
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(actions))
	}
	if actions[0].GetCode() != "RESTART_VNFC" ||
		actions[0].GetMoInstance() != "ims.a" || actions[0].GetOp() != analysis.OpReplace {
		t.Errorf("action = %+v", actions[0])
	}
	if actions[0].GetValue().GetStringValue() != "RESTART" {
		t.Errorf("action value = %v", actions[0].GetValue())
	}
	if actions[1].GetValue().GetNumberValue() != 3 {
		t.Errorf("action value = %v", actions[1].GetValue())
	}
	if len(causes[1].GetActions()) != 0 {
		t.Errorf("second cause actions = %d, want 0", len(causes[1].GetActions()))
	}

	missing := got.GetMeta().GetMissingContext()
	if len(missing) != 2 {
		t.Fatalf("missing context = %d, want 2", len(missing))
	}
	if missing[0].GetProvider() != analysis.ProviderConfiguration || missing[0].GetEntity() != "ims.a" ||
		missing[0].GetKey() != "k" || missing[0].GetReason() != analysis.ReasonTimeout {
		t.Errorf("missing[0] = %+v", missing[0])
	}
	// The reason is what separates "the row does not exist" from "the backend
	// was unreachable", and only one of those is the operator's problem.
	if missing[1].GetReason() != analysis.ReasonNotFound {
		t.Errorf("missing[1].reason = %q", missing[1].GetReason())
	}
}

func TestActionValueCoversEveryJSONType(t *testing.T) {
	tests := []struct {
		name  string
		value any
		check func(t *testing.T, v *structpb.Value)
	}{
		{
			name:  "string",
			value: "RESTART",
			check: func(t *testing.T, v *structpb.Value) {
				if v.GetStringValue() != "RESTART" {
					t.Errorf("= %v", v)
				}
			},
		},
		{
			name:  "number",
			value: 3,
			check: func(t *testing.T, v *structpb.Value) {
				if v.GetNumberValue() != 3 {
					t.Errorf("= %v", v)
				}
			},
		},
		{
			name:  "float",
			value: 1.5,
			check: func(t *testing.T, v *structpb.Value) {
				if v.GetNumberValue() != 1.5 {
					t.Errorf("= %v", v)
				}
			},
		},
		{
			name:  "bool",
			value: true,
			check: func(t *testing.T, v *structpb.Value) {
				if !v.GetBoolValue() {
					t.Errorf("= %v", v)
				}
			},
		},
		{
			// An action without a value leaves the field unset. Not every
			// action takes one — RESTART_VNFC says all it means in its code —
			// and an explicit null would tell a caller to set something to
			// null, which is a different instruction from having nothing to
			// set. The rule side cannot express that instruction anyway: GRL
			// has no nil literal, so a value is either given or absent.
			name:  "no value",
			value: nil,
			check: func(t *testing.T, v *structpb.Value) {
				if v != nil {
					t.Errorf("= %v, want the field left unset", v)
				}
			},
		},
		{
			name:  "object",
			value: map[string]any{"zone": "a", "port": 5060},
			check: func(t *testing.T, v *structpb.Value) {
				if v.GetStructValue().GetFields()["zone"].GetStringValue() != "a" {
					t.Errorf("= %v", v)
				}
			},
		},
		{
			name:  "array",
			value: []any{"a", 2.0, false},
			check: func(t *testing.T, v *structpb.Value) {
				if len(v.GetListValue().GetValues()) != 3 {
					t.Errorf("= %v", v)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := analysis.AnalysisResult{
				OverallStatus: analysis.RCAStatusComplete,
				ContextStatus: analysis.StatusComplete,
				RCAStatus:     analysis.RCAStatusComplete,
				RootCauses: []analysis.RootCause{{
					ID: "rc-1", Actions: []analysis.RecommendedAction{{Code: "C", Value: tc.value}},
				}},
			}
			got, err := responseToPB(result)
			if err != nil {
				t.Fatalf("responseToPB: %v", err)
			}
			tc.check(t, got.GetRca().GetRootCauses()[0].GetActions()[0].GetValue())
		})
	}
}

func TestResponseToPBRejectsUnrepresentableValue(t *testing.T) {
	// The Result sink already rejects these, so this path is unreachable
	// through the rule engine. It is still an error rather than a panic or a
	// dropped field: an action shipped without its value proposes a change
	// without saying what to change it to.
	result := analysis.AnalysisResult{
		OverallStatus: analysis.RCAStatusComplete,
		ContextStatus: analysis.StatusComplete,
		RCAStatus:     analysis.RCAStatusComplete,
		RootCauses: []analysis.RootCause{{
			ID: "rc-1", Actions: []analysis.RecommendedAction{{Code: "BAD", Value: make(chan int)}},
		}},
	}

	got, err := responseToPB(result)
	if err == nil {
		t.Fatal("responseToPB = nil, want an error")
	}
	if got != nil {
		t.Errorf("response = %v, want nil", got)
	}
	if !strings.Contains(err.Error(), "BAD") || !strings.Contains(err.Error(), "rc-1") {
		t.Errorf("error = %q, want it to name the action and its root cause", err)
	}
}

func TestResponseToPBOnAnEmptyResult(t *testing.T) {
	// NO_CONCLUSION over a complete context: a real answer with nothing in it.
	got, err := responseToPB(analysis.AnalysisResult{
		RequestID: "req-1", Incident: "inc-1",
		OverallStatus: analysis.RCAStatusNoConclusion,
		ContextStatus: analysis.StatusComplete,
		RCAStatus:     analysis.RCAStatusNoConclusion,
	})
	if err != nil {
		t.Fatalf("responseToPB: %v", err)
	}
	if got.GetRca() == nil {
		t.Error("rca is nil; the message must exist even with no causes")
	}
	if len(got.GetRca().GetRootCauses()) != 0 {
		t.Errorf("root causes = %d, want 0", len(got.GetRca().GetRootCauses()))
	}
	if got.GetMeta().GetContextStatus() != analysis.StatusComplete {
		t.Errorf("meta.context_status = %q", got.GetMeta().GetContextStatus())
	}
}
