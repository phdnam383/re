package ruleengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"re/internal/analysis"
)

type scenario struct {
	name          string
	contextGolden string
}

var scenarios = []scenario{
	{name: "link_to_sipgw_down", contextGolden: "sipgw_link_down.json"},
	{name: "link_to_diagw_down", contextGolden: "diagw_link_down.json"},
	{name: "tps_overloaded", contextGolden: "tps_overloaded.json"},
}

func shippedRules(t *testing.T) []analysis.RuleDefinition {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "grule", "*.grl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no .grl files found")
	}
	out := make([]analysis.RuleDefinition, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".grl")
		out = append(out, analysis.RuleDefinition{
			ID: "rule-" + name, Name: name, Content: string(content), Salience: 100,
		})
	}
	return out
}

func loadContextGolden(t *testing.T, name string) analysis.ContextSnapshot {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "context_builder", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap analysis.ContextSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func analyseScenario(t *testing.T, snap analysis.ContextSnapshot) analysis.RCAResult {
	t.Helper()
	e := newEngine(t, Options{Rules: fakeRepo{rules: shippedRules(t)}})
	got, err := e.Analyze(context.Background(), snap)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return got
}

func TestScenarioGolden(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			assertRCAGolden(t, sc.name, analyseScenario(t, loadContextGolden(t, sc.contextGolden)))
		})
	}
}

func TestScenarioEveryDocumentRunsExactlyOnce(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			got := analyseScenario(t, loadContextGolden(t, sc.contextGolden))
			if len(got.RuleExecutions) != 3 {
				t.Fatalf("executions = %d, want 3", len(got.RuleExecutions))
			}
			for _, execution := range got.RuleExecutions {
				if execution.Status != analysis.RuleStatusComplete || execution.Passes != 1 {
					t.Errorf("%s = %s, passes %d", execution.RuleName, execution.Status, execution.Passes)
				}
			}
		})
	}
}

func TestScenarioSIPGWRestartsEveryDownComponent(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "sipgw_link_down.json"))
	want := map[string]string{
		"I-CSCF load balancer is unavailable":  "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
		"I-CSCF SIP component is unavailable":  "ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1",
		"SIPGW logic component is unavailable": "ims.vdu_cs_logic.vnfc_cs_logic_1",
	}
	assertRestartCauses(t, got, "SIPGW_DOWN", want)
}

func TestScenarioLinkRuleUsesTheAlertingVNFCAsSource(t *testing.T) {
	snap := loadContextGolden(t, "sipgw_link_down.json")
	for i := range snap.Links {
		if snap.Links[i].SrcPath == "ims.vdu_sb_sip_core.vnfc_sb_sip_core_2" {
			snap.Links[i].Status = "UP"
		}
	}

	// The alerting VNFC is source instance 1, whose link is still DOWN. An
	// IsSeveredBetween call using the whole source VDU would see source 2's UP
	// edge and suppress every conclusion; using Alert.SourcePath must not.
	got := analyseScenario(t, snap)
	if len(got.RootCauses) != 3 {
		t.Fatalf("root causes = %d, want 3 for the severed alert-source link", len(got.RootCauses))
	}
}

func TestScenarioDIAGWSparesHealthyConnector(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "diagw_link_down.json"))
	want := map[string]string{
		"DIAGW load balancer is unavailable":   "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1",
		"DIAGW Diameter router is unavailable": "ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1",
		"DIAGW routing logic is unavailable":   "ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1",
	}
	assertRestartCauses(t, got, "DIAGW_DOWN", want)
	for _, cause := range got.RootCauses {
		if strings.Contains(cause.Summary, "HSS connector") {
			t.Error("healthy HSS connector was blamed")
		}
	}
}

func TestScenarioTPSRestartsDownVNFCsAndReducesLogs(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "tps_overloaded.json"))
	if len(got.RootCauses) != 2 {
		t.Fatalf("causes = %d, want 2", len(got.RootCauses))
	}
	restart := got.RootCauses[0]
	if restart.Category != "TPS_OVERLOADED" || len(restart.Components) != 2 {
		t.Fatalf("restart cause = %+v", restart)
	}
	for _, component := range restart.Components {
		if component.Action == nil || component.Action.Code != "RESTART_VNFC" {
			t.Errorf("component = %+v", component)
		}
	}
	config := got.RootCauses[1]
	if config.Category != "HIGH_LOG_FILE_CONFIG" || config.Role != analysis.RoleContributing {
		t.Fatalf("config cause = %+v", config)
	}
	component := config.Components[0]
	if component.Entity != "ims.vdu_sb_logic.vnfc_sb_logic_1" ||
		component.Action.MOInstance != "ims.vdu_sb_logic.vnfc_sb_logic_1_num_of_log_file" {
		t.Errorf("config component = %+v", component)
	}
	value := component.Action.Value
	if numericValue(value) != 3 {
		t.Errorf("config value = %#v", value)
	}
}

func numericValue(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int:
		return float64(number)
	case int32:
		return float64(number)
	case int64:
		return float64(number)
	default:
		return 0
	}
}

func TestScenarioRulesNameNoVNFCInstance(t *testing.T) {
	for _, rule := range shippedRules(t) {
		if strings.Contains(rule.Content, ".vnfc_") {
			t.Errorf("%s hard-codes a VNFC instance", rule.Name)
		}
	}
}

func TestScenarioPartialContextStillProducesCauses(t *testing.T) {
	snap := loadContextGolden(t, "sipgw_link_down.json")
	snap.Status = analysis.StatusPartial
	got := analyseScenario(t, snap)
	if got.Status != analysis.RCAStatusPartial || len(got.RootCauses) != 3 {
		t.Errorf("result = %+v", got)
	}
}

func TestScenarioIsDeterministic(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			snap := loadContextGolden(t, sc.contextGolden)
			var first string
			for i := range 5 {
				encoded, err := json.Marshal(analyseScenario(t, snap))
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = string(encoded)
				} else if string(encoded) != first {
					t.Fatalf("run %d is not deterministic", i)
				}
			}
		})
	}
}

func assertRestartCauses(t *testing.T, got analysis.RCAResult, category string, want map[string]string) {
	t.Helper()
	if len(got.RootCauses) != len(want) {
		t.Fatalf("causes = %d, want %d", len(got.RootCauses), len(want))
	}
	for _, cause := range got.RootCauses {
		entity, ok := want[cause.Summary]
		if !ok || cause.Category != category || cause.Role != analysis.RolePrimary {
			t.Errorf("cause = %+v", cause)
			continue
		}
		if len(cause.Components) != 1 || cause.Components[0].Entity != entity ||
			cause.Components[0].Action == nil || cause.Components[0].Action.Code != "RESTART_VNFC" {
			t.Errorf("components = %+v", cause.Components)
		}
	}
}

func assertRCAGolden(t *testing.T, scenario string, got analysis.RCAResult) {
	t.Helper()
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "testdata", "rca", scenario, "result.json")
	if os.Getenv("RE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(want) {
		t.Errorf("result does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, encoded)
	}
}
