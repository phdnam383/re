package ruleengine

import (
	"strings"
	"testing"

	"re/internal/analysis"
)

func TestAssertMergesByCategoryRoleAndSummary(t *testing.T) {
	r := NewResult()
	r.Assert("SIPGW_DOWN", "primary", "SIP unavailable")
	r.RecommendRestartVNFC([]string{"ims.vdu_a.vnfc_a_2"})
	r.Assert("SIPGW_DOWN", "PRIMARY", "SIP unavailable")
	r.RecommendRestartVNFC([]string{"ims.vdu_a.vnfc_a_1"})

	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	causes := r.RootCauses()
	if len(causes) != 1 {
		t.Fatalf("causes = %d, want 1", len(causes))
	}
	if len(causes[0].Components) != 2 {
		t.Fatalf("components = %d, want 2", len(causes[0].Components))
	}
	if causes[0].Components[0].Entity != "ims.vdu_a.vnfc_a_1" {
		t.Errorf("components are not deterministic: %+v", causes[0].Components)
	}
}

func TestAssertKeepsDifferentSummariesSeparate(t *testing.T) {
	r := NewResult()
	r.Assert("SIPGW_DOWN", "PRIMARY", "load balancer unavailable")
	r.RecommendRestartVNFC([]string{"ims.lb.1"})
	r.Assert("SIPGW_DOWN", "PRIMARY", "logic unavailable")
	r.RecommendRestartVNFC([]string{"ims.logic.1"})
	if got := len(r.RootCauses()); got != 2 {
		t.Fatalf("causes = %d, want 2", got)
	}
}

func TestAssertValidation(t *testing.T) {
	tests := []struct {
		category, role, summary string
		want                    string
	}{
		{"", "PRIMARY", "summary", "category"},
		{"C", "NOPE", "summary", "role"},
		{"C", "PRIMARY", "", "summary"},
	}
	for _, tc := range tests {
		r := NewResult()
		r.Assert(tc.category, tc.role, tc.summary)
		if err := r.Err(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Err() = %v, want %q", err, tc.want)
		}
	}
}

func TestRecommendRequiresAssertInTheSameRuleScope(t *testing.T) {
	r := NewResult()
	r.currentRule = func() string { return "RuleB" }
	r.activeByRule["RuleA"] = rootCauseKey{category: "C", role: "PRIMARY", summary: "s"}
	r.RecommendRestartVNFC([]string{"ims.a"})
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "no successful Assert") {
		t.Fatalf("Err() = %v", err)
	}
}

func TestRecommendRestartVNFCDeduplicates(t *testing.T) {
	r := NewResult()
	r.Assert("C", "PRIMARY", "s")
	r.RecommendRestartVNFC([]string{"ims.a", "ims.a"})
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	c := r.RootCauses()[0].Components
	if len(c) != 1 || c[0].Action.Code != "RESTART_VNFC" || c[0].Action.Op != analysis.OpReplace {
		t.Errorf("components = %+v", c)
	}
}

func TestRecommendSetConfigCarriesScalarValue(t *testing.T) {
	r := NewResult()
	r.Assert("HIGH_LOG_FILE_CONFIG", "CONTRIBUTING", "high logs")
	r.RecommendSetConfig("ims.a", "ims.a_num_of_log_file", 3)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	component := r.RootCauses()[0].Components[0]
	if component.Entity != "ims.a" || component.Action.MOInstance != "ims.a_num_of_log_file" {
		t.Errorf("component = %+v", component)
	}
	a := component.Action
	if numericValue(a.Value) != 3 {
		t.Errorf("value = %#v", a.Value)
	}
}

func TestComponentRejectsConflictingActions(t *testing.T) {
	first := NewResult()
	first.Assert("C", "PRIMARY", "s")
	first.RecommendRestartVNFC([]string{"ims.a"})

	second := NewResult()
	second.Assert("C", "PRIMARY", "s")
	second.RecommendSetConfig("ims.a", "ims.a_num_of_log_file", 1)

	merged := first.causes.clone()
	if err := merged.mergeFrom(second.causes); err == nil || !strings.Contains(err.Error(), "conflicting actions") {
		t.Fatalf("merge = %v", err)
	}
}

func TestCauseSetMergeUnionsComponents(t *testing.T) {
	first := NewResult()
	first.Assert("C", "PRIMARY", "s")
	first.RecommendRestartVNFC([]string{"ims.a"})
	second := NewResult()
	second.Assert("C", "PRIMARY", "s")
	second.RecommendRestartVNFC([]string{"ims.b"})

	merged := first.causes.clone()
	if err := merged.mergeFrom(second.causes); err != nil {
		t.Fatal(err)
	}
	if got := len(merged.finalize()[0].Components); got != 2 {
		t.Errorf("components = %d, want 2", got)
	}
}
