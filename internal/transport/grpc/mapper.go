package grpc

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

// --- request -------------------------------------------------------------

// requestFromPB converts the wire request into the domain input.
//
// It converts and does not judge. A nil request, a nil alert or an empty field
// all survive this function and are rejected by analysis.ContextInput.Validate,
// so the rules a caller is held to are the same whichever entry point it
// arrived through. Returning an error here instead would make gRPC callers
// subject to one set of rules and every other caller to another.
func requestFromPB(req *mdafv1.AnalyzeIncidentRequest) analysis.ContextInput {
	if req == nil {
		return analysis.ContextInput{}
	}

	in := analysis.ContextInput{
		RequestID: req.GetRequestId(),
		Incident:  req.GetIncident(),
	}
	if len(req.GetAlerts()) == 0 {
		return in
	}

	in.Alerts = make([]analysis.Alert, 0, len(req.GetAlerts()))
	for _, a := range req.GetAlerts() {
		// A nil element in a repeated field is not valid protobuf, but a
		// hand-rolled client can send one. It becomes a zero alert, which
		// fails validation on its empty id — never a panic.
		in.Alerts = append(in.Alerts, analysis.Alert{
			ID:                    a.GetId(),
			SourcePath:            a.GetSourcePath(),
			AlertType:             a.GetAlertType(),
			ProbableCause:         a.GetProbableCause(),
			PerceivedSeverity:     a.GetPerceivedSeverity(),
			State:                 a.GetState(),
			CreatedAt:             a.GetCreatedAt(),
			AdditionalInformation: additionalInformationFromPB(a.GetAdditionalInformation()),
		})
	}
	return in
}

// additionalInformationFromPB decodes the TS 28.111 name-value pairs.
//
// structpb.Struct.AsMap does the conversion, which matters more than it looks:
// selector matching compares typed JSON values, so 85 and "85" are different
// and null is a value a selector can match on. Re-encoding through a hand
// written switch would be a second implementation of that mapping, and the
// first thing it would lose is null.
//
// A nil Struct becomes a nil map rather than an empty one. The selector
// distinguishes "the key is absent" from "the key is present and empty", and
// AsMap on a nil Struct already yields an empty map — spelling nil out here
// keeps an alert with no additional information from looking like one that
// carries an empty object.
func additionalInformationFromPB(s *structpb.Struct) map[string]any {
	if s == nil || len(s.GetFields()) == 0 {
		return nil
	}
	return s.AsMap()
}

// --- response ------------------------------------------------------------

// responseToPB converts the domain result into the wire response.
func responseToPB(result analysis.AnalysisResult) (*mdafv1.AnalyzeIncidentResponse, error) {
	rootCauses, err := rootCausesToPB(result.RootCauses)
	if err != nil {
		return nil, err
	}

	return &mdafv1.AnalyzeIncidentResponse{
		RequestId: result.RequestID,
		Incident:  result.Incident,
		Status: &mdafv1.AnalysisStatus{
			Overall: result.OverallStatus,
			Context: result.ContextStatus,
			Rca:     result.RCAStatus,
		},
		Rca: &mdafv1.RootCauseAnalysis{RootCauses: rootCauses},
		Meta: &mdafv1.AnalysisMeta{
			// The same value as status.context, restated where a caller
			// reading meta expects to find it. Deriving it from anywhere else
			// would let the two disagree.
			ContextStatus:  result.ContextStatus,
			MissingContext: missingContextToPB(result.MissingContext),
		},
	}, nil
}

// rootCausesToPB maps the causes, preserving the order the rule engine fixed.
func rootCausesToPB(causes []analysis.RootCause) ([]*mdafv1.RootCause, error) {
	if len(causes) == 0 {
		return nil, nil
	}

	out := make([]*mdafv1.RootCause, 0, len(causes))
	for _, c := range causes {
		actions, err := actionsToPB(c.ID, c.Actions)
		if err != nil {
			return nil, err
		}
		out = append(out, &mdafv1.RootCause{
			Id:         c.ID,
			Category:   c.Category,
			Summary:    c.Summary,
			Entity:     c.Entity,
			Role:       c.Role,
			Confidence: c.Confidence,
			Actions:    actions,
		})
	}
	return out, nil
}

func actionsToPB(causeID string, actions []analysis.RecommendedAction) ([]*mdafv1.RecommendedAction, error) {
	if len(actions) == 0 {
		return nil, nil
	}

	out := make([]*mdafv1.RecommendedAction, 0, len(actions))
	for _, a := range actions {
		// structpb.NewValue carries the JSON type through, including null:
		// a nil value becomes a protobuf NullValue rather than an absent
		// field, so a caller can tell "set this to null" from "no value was
		// given".
		value, err := structpb.NewValue(a.Value)
		if err != nil {
			// The Result sink already rejected values that cannot be JSON, so
			// this is unreachable through the rule path. It is still an error
			// rather than a panic or a dropped field: shipping an action with
			// its value silently missing would propose a change without
			// saying what to change it to.
			return nil, fmt.Errorf("map action %q of root cause %q: value %T is not representable: %w",
				a.Code, causeID, a.Value, err)
		}
		out = append(out, &mdafv1.RecommendedAction{
			Code:       a.Code,
			Priority:   a.Priority,
			MoInstance: a.MOInstance,
			Path:       a.Path,
			Op:         a.Op,
			Value:      value,
		})
	}
	return out, nil
}

func missingContextToPB(missing []analysis.MissingContext) []*mdafv1.MissingContext {
	if len(missing) == 0 {
		return nil
	}

	out := make([]*mdafv1.MissingContext, 0, len(missing))
	for _, m := range missing {
		// Reason travels with the gap. Provider and entity alone cannot
		// separate "the row does not exist" from "the backend was
		// unreachable", and only one of those is the operator's problem.
		out = append(out, &mdafv1.MissingContext{
			Provider: m.Provider,
			Entity:   m.Entity,
			Key:      m.Key,
			Reason:   m.Reason,
		})
	}
	return out
}
