package contextbuilder

import (
	"testing"

	"re/internal/analysis"
)

func TestSelectorMatchesAlert(t *testing.T) {
	alert := analysis.Alert{
		ID:            "a1",
		AlertType:     "QUALITY_OF_SERVICE_ALERT",
		ProbableCause: "THRESHOLD_CROSSING",
		AdditionalInformation: map[string]any{
			"metric":          "overload_ram",
			"observed_value":  93.5,
			"threshold_value": float64(85),
			"acknowledged":    false,
			"cleared_at":      nil,
		},
	}

	tests := []struct {
		name     string
		selector Selector
		want     bool
	}{
		{
			name:     "probable cause matches",
			selector: Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
			want:     true,
		},
		{
			name:     "probable cause is one of several",
			selector: Selector{ProbableCauses: []string{"LINK_DOWN", "THRESHOLD_CROSSING"}},
			want:     true,
		},
		{
			name:     "probable cause misses",
			selector: Selector{ProbableCauses: []string{"LINK_DOWN"}},
			want:     false,
		},
		{
			name:     "probable cause is case-insensitive",
			selector: Selector{ProbableCauses: []string{"threshold_crossing"}},
			want:     true,
		},
		{
			name: "clauses are ANDed",
			selector: Selector{
				ProbableCauses: []string{"THRESHOLD_CROSSING"},
				AlertTypes:     []string{"EQUIPMENT_ALERT"},
			},
			want: false,
		},
		{
			name: "both clauses satisfied",
			selector: Selector{
				ProbableCauses: []string{"THRESHOLD_CROSSING"},
				AlertTypes:     []string{"quality_of_service_alert"},
			},
			want: true,
		},
		{
			name:     "additional information value matches case-insensitively",
			selector: Selector{AdditionalInformation: map[string][]any{"metric": {"OVERLOAD_RAM"}}},
			want:     true,
		},
		{
			name:     "additional information value misses",
			selector: Selector{AdditionalInformation: map[string][]any{"metric": {"overload_cpu"}}},
			want:     false,
		},
		{
			name:     "empty value list asserts key presence only",
			selector: Selector{AdditionalInformation: map[string][]any{"observed_value": {}}},
			want:     true,
		},
		{
			name:     "absent key never matches",
			selector: Selector{AdditionalInformation: map[string][]any{"absent": {}}},
			want:     false,
		},
		{
			name:     "keys are case-sensitive",
			selector: Selector{AdditionalInformation: map[string][]any{"Metric": {"overload_ram"}}},
			want:     false,
		},
		{
			name:     "number matches a number",
			selector: Selector{AdditionalInformation: map[string][]any{"threshold_value": {float64(85)}}},
			want:     true,
		},
		{
			name:     "string does not match a number",
			selector: Selector{AdditionalInformation: map[string][]any{"threshold_value": {"85"}}},
			want:     false,
		},
		{
			name:     "number does not match a string",
			selector: Selector{AdditionalInformation: map[string][]any{"metric": {float64(1)}}},
			want:     false,
		},
		{
			name:     "boolean matches a boolean",
			selector: Selector{AdditionalInformation: map[string][]any{"acknowledged": {false}}},
			want:     true,
		},
		{
			name:     "boolean does not match the string form",
			selector: Selector{AdditionalInformation: map[string][]any{"acknowledged": {"false"}}},
			want:     false,
		},
		{
			name:     "null matches a null",
			selector: Selector{AdditionalInformation: map[string][]any{"cleared_at": {nil}}},
			want:     true,
		},
		{
			name: "values within one key are ORed",
			selector: Selector{AdditionalInformation: map[string][]any{
				"metric": {"overload_cpu", "overload_ram"},
			}},
			want: true,
		},
		{
			name: "keys are ANDed",
			selector: Selector{AdditionalInformation: map[string][]any{
				"metric":          {"overload_ram"},
				"threshold_value": {float64(99)},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.selector.MatchesAlert(alert); got != tt.want {
				t.Errorf("MatchesAlert() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The conjunction is inside one alert. Two alerts that between them satisfy
// every clause must not match — a profile describes a kind of alert, not a bag
// of them.
func TestSelectorConjunctionIsPerAlert(t *testing.T) {
	selector := Selector{
		ProbableCauses: []string{"THRESHOLD_CROSSING"},
		AlertTypes:     []string{"QUALITY_OF_SERVICE_ALERT"},
	}

	split := []analysis.Alert{
		{ID: "a1", ProbableCause: "THRESHOLD_CROSSING", AlertType: "EQUIPMENT_ALERT"},
		{ID: "a2", ProbableCause: "LINK_DOWN", AlertType: "QUALITY_OF_SERVICE_ALERT"},
	}
	if selector.Matches(split) {
		t.Error("clauses satisfied by different alerts must not match")
	}

	together := append(split, analysis.Alert{
		ID: "a3", ProbableCause: "THRESHOLD_CROSSING", AlertType: "QUALITY_OF_SERVICE_ALERT",
	})
	if !selector.Matches(together) {
		t.Error("one alert satisfying every clause must match")
	}
}

func TestSelectorMatchesEmptyAlertList(t *testing.T) {
	selector := Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}}
	if selector.Matches(nil) {
		t.Error("no alerts cannot satisfy a selector")
	}
}
