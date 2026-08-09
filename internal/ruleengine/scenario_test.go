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

// End-to-end scenario tests over the rule documents that actually ship.
//
// The input is not hand-built here: each scenario reads the Context Builder's
// own golden snapshot. That is what makes these tests meaningful across the
// seam — if the builder's output shape drifts, the rules stop seeing what they
// were written against, and a hand-copied fixture would keep passing while
// production broke.
//
// Every scenario runs the whole rule set, not just its own document. A scenario
// that accidentally matches another operator's rules is exactly the failure
// worth catching, and it is invisible if each test only loads the rules it
// expects to fire.

// scenario names the three fixtures. The context-builder golden and the RCA
// golden are separate files because they are outputs of separate stages.
type scenario struct {
	name          string
	contextGolden string
}

var scenarios = []scenario{
	{name: "link_to_sipgw_down", contextGolden: "sipgw_link_down.json"},
	{name: "link_to_diagw_down", contextGolden: "diagw_link_down.json"},
	{name: "tps_overloaded", contextGolden: "tps_overloaded.json"},
}

// shippedRules loads re/grule/*.grl as rule definitions.
//
// The ids and salience are fixed here rather than read from the database: a
// golden file cannot contain gen_random_uuid() output, and the seed gives all
// three rows the same salience, so the name tie-break decides the order either
// way.
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
			ID:       "rule-" + name,
			Name:     name,
			Content:  string(content),
			Salience: 100,
		})
	}
	return out
}

// loadContextGolden reads a snapshot the Context Builder produced.
func loadContextGolden(t *testing.T, name string) analysis.ContextSnapshot {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "context_builder", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var snap analysis.ContextSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode %s: %v", path, err)
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
			got := analyseScenario(t, loadContextGolden(t, sc.contextGolden))
			assertRCAGolden(t, sc.name, got)
		})
	}
}

// --- explicit assertions -------------------------------------------------
//
// The golden file locks the whole result, but a golden that was regenerated
// after a regression still passes. These state the outcome each scenario is
// supposed to have, so a wrong answer has to be argued with rather than
// re-recorded.

func TestScenarioSIPGWBlamesEveryTerminatedComponent(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "sipgw_link_down.json"))

	if got.Status != analysis.RCAStatusComplete {
		t.Errorf("status = %s, want COMPLETE", got.Status)
	}

	want := []struct {
		id, category, entity, role string
		confidence                 float64
	}{
		{"rc-sipgw-loadbalancer-down", "SIPGW_DOWN", "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1", analysis.RolePrimary, 0.35},
		{"rc-sipgw-icscf-down", "SIPGW_DOWN", "ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1", analysis.RolePrimary, 0.35},
		{"rc-sipgw-logic-down", "SIPGW_DOWN", "ims.vdu_cs_logic.vnfc_cs_logic_1", analysis.RolePrimary, 0.35},
	}
	if len(got.RootCauses) != len(want) {
		t.Fatalf("root causes = %d, want %d: %v", len(got.RootCauses), len(want), causeIDsOf(got))
	}
	for i, w := range want {
		c := got.RootCauses[i]
		if c.ID != w.id || c.Category != w.category || c.Entity != w.entity ||
			c.Role != w.role || c.Confidence != w.confidence {
			t.Errorf("root cause %d = %+v, want %+v", i, c, w)
		}
		// Each blamed component gets exactly one restart proposal, aimed at
		// itself.
		if len(c.Actions) != 1 {
			t.Fatalf("root cause %s has %d actions, want 1", c.ID, len(c.Actions))
		}
		a := c.Actions[0]
		if a.Code != "RESTART_VNFC" || a.MOInstance != w.entity ||
			a.Path != "lifecycle.action" || a.Op != analysis.OpReplace || a.Value != "RESTART" {
			t.Errorf("root cause %s action = %+v", c.ID, a)
		}
		if a.Priority != 90 {
			t.Errorf("root cause %s action priority = %v, want 90", c.ID, a.Priority)
		}
	}
}

func TestScenarioDIAGWSparesTheHealthyConnector(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "diagw_link_down.json"))

	if got.Status != analysis.RCAStatusComplete {
		t.Errorf("status = %s, want COMPLETE", got.Status)
	}

	// The document holds four rules. cs_hss_connector_1 is RUNNING, so the
	// fourth must not fire — this is the test that a rule set does not blame
	// everything in the dependency chain by reflex.
	want := []string{
		"rc-diagw-loadbalancer-down",
		"rc-diagw-diameter-router-down",
		"rc-diagw-logic-down",
	}
	if got := causeIDsOf(got); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("root causes = %v, want %v", got, want)
	}

	// Salience decides the order, and the load balancer is the one the rule
	// author was most sure about.
	if got.RootCauses[0].Confidence != 0.55 {
		t.Errorf("load balancer confidence = %v, want 0.55", got.RootCauses[0].Confidence)
	}
	for _, c := range got.RootCauses[1:] {
		if c.Confidence != 0.35 {
			t.Errorf("%s confidence = %v, want 0.35", c.ID, c.Confidence)
		}
	}
}

func TestScenarioTPSCombinesDegradationAndConfiguration(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "tps_overloaded.json"))

	if got.Status != analysis.RCAStatusComplete {
		t.Errorf("status = %s, want COMPLETE", got.Status)
	}

	want := []string{"rc-tps-replica-degradation", "rc-tps-high-log-file-config"}
	if ids := causeIDsOf(got); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("root causes = %v, want %v", ids, want)
	}

	// A trigger and a contributing factor, not two triggers: the replica loss
	// is what concentrated the load, the log-file setting is what made the
	// remaining replica's RAM worse.
	if got.RootCauses[0].Role != analysis.RolePrimary {
		t.Errorf("role = %s, want PRIMARY", got.RootCauses[0].Role)
	}
	if got.RootCauses[1].Role != analysis.RoleContributing {
		t.Errorf("role = %s, want CONTRIBUTING", got.RootCauses[1].Role)
	}

	// The action values carry their JSON types through: a replica count and a
	// configuration value are both numbers, and the response maps them to a
	// protobuf Value.
	restore := got.RootCauses[0].Actions[0]
	if restore.Code != "RESTORE_REPLICAS" || restore.Path != "replicas" || restore.Priority != 100 {
		t.Errorf("restore action = %+v", restore)
	}
	if v, ok := restore.Value.(int64); !ok || v != 3 {
		t.Errorf("restore value = %#v, want int64(3)", restore.Value)
	}
}

func TestScenarioEveryRuleDocumentRunsForEveryScenario(t *testing.T) {
	// A document that matched nothing is COMPLETE with no causes. Confirming
	// that here is what separates "the other scenarios' rules were evaluated
	// and stayed silent" from "they were never reached".
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			got := analyseScenario(t, loadContextGolden(t, sc.contextGolden))

			if len(got.RuleExecutions) != 3 {
				t.Fatalf("executions = %d, want 3", len(got.RuleExecutions))
			}
			for _, ex := range got.RuleExecutions {
				if ex.Status != analysis.RuleStatusComplete {
					t.Errorf("%s = %s (%s), want COMPLETE", ex.RuleName, ex.Status, ex.Error)
				}
				if ex.RuleName == sc.name {
					if ex.RootCauseCount == 0 {
						t.Errorf("%s concluded nothing in its own scenario", ex.RuleName)
					}
					continue
				}
				if ex.RootCauseCount != 0 {
					t.Errorf("%s fired on the %s scenario", ex.RuleName, sc.name)
				}
			}
		})
	}
}

func TestScenarioPartialContextStillProducesCauses(t *testing.T) {
	// The most common partial context is one where the failing entity is
	// exactly the one whose configuration API stopped answering. Refusing to
	// analyse it would withhold the answer precisely when it is needed.
	snap := loadContextGolden(t, "sipgw_link_down.json")
	snap.Status = analysis.StatusPartial
	snap.MissingContext = []analysis.MissingContext{{
		Provider: analysis.ProviderConfiguration,
		Entity:   "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
		Key:      "some_key",
		Reason:   analysis.ReasonTimeout,
	}}

	got := analyseScenario(t, snap)

	if got.Status != analysis.RCAStatusPartial {
		t.Errorf("status = %s, want PARTIAL", got.Status)
	}
	if len(got.RootCauses) != 3 {
		t.Errorf("root causes = %d, want 3 — a partial context still runs every rule", len(got.RootCauses))
	}
}

func TestScenarioIsDeterministic(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			snap := loadContextGolden(t, sc.contextGolden)

			var first string
			for i := range 10 {
				got := analyseScenario(t, snap)
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = string(encoded)
					continue
				}
				if string(encoded) != first {
					t.Fatalf("run %d differs from run 0", i)
				}
			}
		})
	}
}

// --- helpers -------------------------------------------------------------

func causeIDsOf(r analysis.RCAResult) []string {
	out := make([]string, 0, len(r.RootCauses))
	for _, c := range r.RootCauses {
		out = append(out, c.ID)
	}
	return out
}

func assertRCAGolden(t *testing.T, scenario string, got analysis.RCAResult) {
	t.Helper()

	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Join("..", "..", "testdata", "rca", scenario)
	path := filepath.Join(dir, "result.json")

	if os.Getenv("RE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with RE_UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if string(encoded) != string(want) {
		t.Errorf("result does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, encoded)
	}
}
