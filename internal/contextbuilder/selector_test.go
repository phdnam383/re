package contextbuilder

import (
	"testing"

	"re/internal/analysis"
)

func TestMatchesValueString(t *testing.T) {
	// A string selector matches when the value equals it or is a descendant in
	// the path tree, case-insensitively — a VDU path matches its own VNFCs.
	cases := []struct {
		name      string
		want      any
		got       any
		wantMatch bool
	}{
		{"exact case-insensitive", "ABC", "abc", true},
		{"exact mismatch", "ABC", "abd", false},
		{"VDU matches its VNFC", "ims.vdu_sb_h248gw", "ims.vdu_sb_h248gw.vnfc_sb_h248gw_1", true},
		{"VDU matches itself", "ims.vdu_sb_h248gw", "ims.vdu_sb_h248gw", true},
		{"deep descendant", "ims.vdu_cs_loadbalancer_icscf", "ims.vdu_cs_loadbalancer_icscf.vnfc_x.y", true},
		// a sibling that merely shares a prefix is NOT a descendant
		{"sibling prefix no dot", "ims.vdu_cs_loadbalancer_icscf", "ims.vdu_cs_loadbalancer_icscf_other", false},
		{"non-string got", "abc", 123, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesValue(tc.want, tc.got)
			if got != tc.wantMatch {
				t.Fatalf("matchesValue(%v, %v) = %v, want %v", tc.want, tc.got, got, tc.wantMatch)
			}
		})
	}
}

func TestMatchesValueWildcardAccepted(t *testing.T) {
	// A trailing ".*" is still accepted for readability but does not change the
	// result: a VDU path matches itself and its subtree, identical to the bare
	// form. Listing it side by side makes the equivalence explicit.
	base := "ims.vdu_cs_loadbalancer_icscf"
	variants := []struct {
		label string
		want  string
	}{
		{"bare", base},
		{"wildcard", base + ".*"},
	}
	for _, v := range variants {
		t.Run(v.label+"/self", func(t *testing.T) {
			if !matchesValue(v.want, base) {
				t.Fatalf("matchesValue(%q, %q) = false, want true", v.want, base)
			}
		})
		t.Run(v.label+"/descendant", func(t *testing.T) {
			if !matchesValue(v.want, base+".vnfc_x") {
				t.Fatalf("matchesValue(%q, descendant) = false, want true", v.want)
			}
		})
		t.Run(v.label+"/sibling", func(t *testing.T) {
			if matchesValue(v.want, base+"_other") {
				t.Fatalf("matchesValue(%q, sibling) = true, want false", v.want)
			}
		})
	}
}

func TestMatchesValueScalar(t *testing.T) {
	// Numbers, bools and nil stay exact matches — they have no path hierarchy.
	cases := []struct {
		name      string
		want      any
		got       any
		wantMatch bool
	}{
		{"number equal", float64(3), float64(3), true},
		{"number mismatch", float64(3), float64(4), false},
		{"bool equal", true, true, true},
		{"bool mismatch", true, false, false},
		{"nil equal", nil, nil, true},
		{"nil vs string", nil, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesValue(tc.want, tc.got)
			if got != tc.wantMatch {
				t.Fatalf("matchesValue(%v, %v) = %v, want %v", tc.want, tc.got, got, tc.wantMatch)
			}
		})
	}
}

func TestSourcePathsMatching(t *testing.T) {
	for _, tc := range []struct {
		name   string
		paths  []string
		source string
		match  bool
	}{
		{"omitted", nil, "ims.vdu_other.vnfc_1", true},
		{"empty", []string{}, "ims.vdu_other.vnfc_1", true},
		{"VDU itself", []string{"ims.vdu_sb_logic"}, "ims.vdu_sb_logic", true},
		{"VNFC", []string{"ims.vdu_sb_logic"}, "ims.vdu_sb_logic.vnfc_sb_logic_1", true},
		{"case insensitive", []string{"IMS.VDU_SB_LOGIC"}, "ims.vdu_sb_logic.vnfc_1", true},
		{"any listed VDU", []string{"ims.vdu_sb_dns", "ims.vdu_sb_logic"}, "ims.vdu_sb_logic.vnfc_1", true},
		{"different VDU", []string{"ims.vdu_sb_logic"}, "ims.vdu_sb_dns.vnfc_1", false},
		{"sibling prefix", []string{"ims.vdu_sb_logic"}, "ims.vdu_sb_logic_other.vnfc_1", false},
		{"different namespace", []string{"ims.vdu_sb_logic"}, "other.vdu_sb_logic.vnfc_1", false},
		{"parent only", []string{"ims.vdu_sb_logic"}, "ims", false},
		{"missing source", []string{"ims.vdu_sb_logic"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Selector{ProbableCauses: []string{"OVERLOAD_CPU"}, SourcePaths: tc.paths}
			a := analysis.Alert{
				ProbableCause: "OVERLOAD_CPU", SourcePath: tc.source,
				AdditionalInformation: map[string]any{"source_path": "ims.vdu_sb_logic.vnfc_1"},
			}
			if got := s.MatchesAlert(a); got != tc.match {
				t.Fatalf("MatchesAlert() = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestSourcePathsClausesMustMatchSameAlert(t *testing.T) {
	s := Selector{
		SourcePaths:           []string{"ims.vdu_sb_logic"},
		ProbableCauses:        []string{"OVERLOAD_CPU"},
		AlertTypes:            []string{"QUALITY_OF_SERVICE_ALERT"},
		AdditionalInformation: map[string][]any{"metric": {"cpu"}},
	}
	matching := analysis.Alert{
		SourcePath: "ims.vdu_sb_logic.vnfc_1", ProbableCause: "OVERLOAD_CPU",
		AlertType: "QUALITY_OF_SERVICE_ALERT", AdditionalInformation: map[string]any{"metric": "cpu"},
	}
	wrongSource, wrongCause, wrongType, wrongInfo := matching, matching, matching, matching
	wrongSource.SourcePath = "ims.vdu_sb_dns.vnfc_1"
	wrongCause.ProbableCause = "OVERLOAD_RAM"
	wrongType.AlertType = "COMMUNICATIONS_ALERT"
	wrongInfo.AdditionalInformation = map[string]any{"metric": "ram"}
	alerts := []analysis.Alert{wrongSource, wrongCause, wrongType, wrongInfo}
	if s.Matches(alerts) {
		t.Fatal("clauses matching different alerts must not select the profile")
	}
	if !s.Matches(append(alerts, matching)) {
		t.Fatal("one alert satisfying all clauses must select the profile")
	}
}
