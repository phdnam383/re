package grpc

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

func requestFromPB(req *mdafv1.AnalyzeAlertRequest) analysis.ContextInput {
	if req == nil {
		return analysis.ContextInput{}
	}
	in := analysis.ContextInput{RequestID: req.GetRequestId()}
	if a := req.GetAlert(); a != nil {
		in.Alerts = []analysis.Alert{{
			ID:                    a.GetId(),
			SourcePath:            a.GetSourcePath(),
			AlertType:             a.GetAlertType(),
			ProbableCause:         a.GetProbableCause(),
			PerceivedSeverity:     a.GetPerceivedSeverity(),
			State:                 a.GetState(),
			CreatedAt:             a.GetCreatedAt(),
			AdditionalInformation: additionalInformationFromPB(a.GetAdditionalInformation()),
		}}
	}
	return in
}

func additionalInformationFromPB(s *structpb.Struct) map[string]any {
	if s == nil || len(s.GetFields()) == 0 {
		return nil
	}
	return s.AsMap()
}

func responseToPB(result analysis.AnalysisResult) (*mdafv1.AnalyzeAlertResponse, error) {
	rootCauses, err := rootCausesToPB(result.RootCauses)
	if err != nil {
		return nil, err
	}
	return &mdafv1.AnalyzeAlertResponse{
		RequestId: result.RequestID,
		Status: &mdafv1.AnalysisStatus{
			Overall: result.OverallStatus,
			Context: result.ContextStatus,
			Rca:     result.RCAStatus,
		},
		Rca: &mdafv1.RootCauseAnalysis{RootCauses: rootCauses},
		Meta: &mdafv1.AnalysisMeta{
			ContextStatus:  result.ContextStatus,
			MissingContext: missingContextToPB(result.MissingContext),
		},
	}, nil
}

func rootCausesToPB(causes []analysis.RootCause) ([]*mdafv1.RootCause, error) {
	if len(causes) == 0 {
		return nil, nil
	}
	out := make([]*mdafv1.RootCause, 0, len(causes))
	for _, cause := range causes {
		components, err := componentsToPB(cause)
		if err != nil {
			return nil, err
		}
		out = append(out, &mdafv1.RootCause{
			Category:   cause.Category,
			Role:       cause.Role,
			Summary:    cause.Summary,
			Components: components,
		})
	}
	return out, nil
}

func componentsToPB(cause analysis.RootCause) ([]*mdafv1.Component, error) {
	if len(cause.Components) == 0 {
		return nil, nil
	}
	out := make([]*mdafv1.Component, 0, len(cause.Components))
	for _, component := range cause.Components {
		action, err := actionToPB(cause, component)
		if err != nil {
			return nil, err
		}
		out = append(out, &mdafv1.Component{Entity: component.Entity, Action: action})
	}
	return out, nil
}

func actionToPB(cause analysis.RootCause, component analysis.Component) (*mdafv1.RecommendedAction, error) {
	if component.Action == nil {
		return nil, nil
	}
	a := component.Action
	var value *structpb.Value
	if a.Value != nil {
		v, err := structpb.NewValue(a.Value)
		if err != nil {
			return nil, fmt.Errorf("map action %q of root cause %q/%q: value %T is not representable: %w",
				a.Code, cause.Category, cause.Summary, a.Value, err)
		}
		value = v
	}
	return &mdafv1.RecommendedAction{
		Code: a.Code, MoInstance: a.MOInstance, Op: a.Op, Value: value,
	}, nil
}

func missingContextToPB(missing []analysis.MissingContext) []*mdafv1.MissingContext {
	if len(missing) == 0 {
		return nil
	}
	out := make([]*mdafv1.MissingContext, 0, len(missing))
	for _, item := range missing {
		out = append(out, &mdafv1.MissingContext{
			Provider: item.Provider, Entity: item.Entity, Key: item.Key, Reason: item.Reason,
		})
	}
	return out
}
