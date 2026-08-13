package grpc

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

func TestRequestFromPBSingularAlert(t *testing.T) {
	info, err := structpb.NewStruct(map[string]any{"metric": "overload_ram"})
	if err != nil {
		t.Fatal(err)
	}
	in := requestFromPB(&mdafv1.AnalyzeAlertRequest{
		RequestId: "req-1",
		Alert: &mdafv1.Alert{
			Id: "a-1", SourcePath: "ims.vdu_a.vnfc_a_1",
			ProbableCause: "THRESHOLD_CROSSING", AdditionalInformation: info,
		},
	})
	if in.RequestID != "req-1" || len(in.Alerts) != 1 {
		t.Fatalf("input = %+v", in)
	}
	if got := in.Alerts[0]; got.ID != "a-1" || got.AdditionalInformation["metric"] != "overload_ram" {
		t.Errorf("alert = %+v", got)
	}
}

func TestRequestFromPBNil(t *testing.T) {
	if got := requestFromPB(nil); got.RequestID != "" || len(got.Alerts) != 0 {
		t.Errorf("input = %+v", got)
	}
}

func TestResponseToPBMapsComponentsAndActions(t *testing.T) {
	result := analysis.AnalysisResult{
		RequestID: "req-1", OverallStatus: analysis.RCAStatusComplete,
		ContextStatus: analysis.StatusComplete, RCAStatus: analysis.RCAStatusComplete,
		RootCauses: []analysis.RootCause{{
			Category: "SIPGW_DOWN", Role: analysis.RolePrimary, Summary: "SIP unavailable",
			Components: []analysis.Component{{
				Entity: "ims.vdu_a.vnfc_a_1",
				Action: &analysis.RecommendedAction{
					Code: "RESTART_VNFC", MOInstance: "ims.vdu_a.vnfc_a_1", Op: analysis.OpReplace,
				},
			}},
		}},
	}
	got, err := responseToPB(result)
	if err != nil {
		t.Fatal(err)
	}
	cause := got.GetRca().GetRootCauses()[0]
	if cause.GetCategory() != "SIPGW_DOWN" || cause.GetRole() != analysis.RolePrimary {
		t.Errorf("cause = %+v", cause)
	}
	component := cause.GetComponents()[0]
	if component.GetEntity() != "ims.vdu_a.vnfc_a_1" || component.GetAction().GetCode() != "RESTART_VNFC" {
		t.Errorf("component = %+v", component)
	}
}

func TestResponseToPBPreservesSetConfigScalarValue(t *testing.T) {
	result := analysis.AnalysisResult{RootCauses: []analysis.RootCause{{
		Category: "HIGH_LOG_FILE_CONFIG", Role: analysis.RoleContributing, Summary: "high logs",
		Components: []analysis.Component{{
			Entity: "ims.vdu_a.vnfc_a_1",
			Action: &analysis.RecommendedAction{
				Code: "SET_CONFIG", MOInstance: "ims.vdu_a.vnfc_a_1_num_of_log_file", Op: analysis.OpReplace,
				Value: 3,
			},
		}},
	}}}
	got, err := responseToPB(result)
	if err != nil {
		t.Fatal(err)
	}
	action := got.GetRca().GetRootCauses()[0].GetComponents()[0].GetAction()
	if action.GetMoInstance() != "ims.vdu_a.vnfc_a_1_num_of_log_file" || action.GetOp() != analysis.OpReplace {
		t.Errorf("action = %v", action)
	}
	value := action.GetValue()
	if value.GetNumberValue() != 3 {
		t.Errorf("value = %v", value)
	}
}
