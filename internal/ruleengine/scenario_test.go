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

	// Root causes come out in subject order, which is the order the snapshot
	// sorts VNFCs in — not salience order. Salience still decides which rule of
	// a document gets its turn first within one pass, but a document that fans
	// out asserts once per instance, and the instances arrive in path order.
	want := []struct {
		id, category, entity, role string
		confidence                 float64
	}{
		{
			"rc-sipgw-loadbalancer-down-ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
			"SIPGW_DOWN", "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1", analysis.RolePrimary, 0.35,
		},
		{
			"rc-sipgw-logic-down-ims.vdu_cs_logic.vnfc_cs_logic_1",
			"SIPGW_DOWN", "ims.vdu_cs_logic.vnfc_cs_logic_1", analysis.RolePrimary, 0.35,
		},
		{
			"rc-sipgw-icscf-down-ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1",
			"SIPGW_DOWN", "ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1", analysis.RolePrimary, 0.35,
		},
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
		// Each blamed instance gets exactly one restart proposal, aimed at
		// itself. The action carries no value: RESTART_VNFC says everything it
		// means in its code.
		if len(c.Actions) != 1 {
			t.Fatalf("root cause %s has %d actions, want 1", c.ID, len(c.Actions))
		}
		a := c.Actions[0]
		if a.Code != "RESTART_VNFC" || a.MOInstance != w.entity ||
			a.Op != analysis.OpReplace || a.Value != nil {
			t.Errorf("root cause %s action = %+v", c.ID, a)
		}
	}
}

func TestScenarioSIPGWNamesNoInstanceInTheRuleText(t *testing.T) {
	// The whole point of the rewrite: the shipped rules address VDUs, and the
	// instances they blame come from the snapshot. A rule text that names a
	// VNFC path is one that goes silent the next time the VDU is scaled, and
	// nothing else in this package would notice.
	for _, r := range shippedRules(t) {
		t.Run(r.Name, func(t *testing.T) {
			if i := strings.Index(r.Content, ".vnfc_"); i >= 0 {
				line := r.Content[max(0, i-80):min(len(r.Content), i+40)]
				t.Errorf("rule text names a VNFC instance:\n...%s...", line)
			}
		})
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
	//
	// The order is the snapshot's VNFC order, so diag_logic precedes
	// diameter_router precedes loadbalancer_diagw.
	want := []string{
		"rc-diagw-logic-down-ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1",
		"rc-diagw-diameter-router-down-ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1",
		"rc-diagw-loadbalancer-down-ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1",
	}
	if got := causeIDsOf(got); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("root causes = %v, want %v", got, want)
	}

	// The load balancer is the one the rule author was most sure about. It is
	// found by id rather than by position: the order now comes from the
	// snapshot, so an index here would be asserting something this test is not
	// about.
	for _, c := range got.RootCauses {
		want := 0.35
		if strings.HasPrefix(c.ID, "rc-diagw-loadbalancer-down-") {
			want = 0.55
		}
		if c.Confidence != want {
			t.Errorf("%s confidence = %v, want %v", c.ID, c.Confidence, want)
		}
	}
}

func TestScenarioTPSCombinesDegradationAndConfiguration(t *testing.T) {
	got := analyseScenario(t, loadContextGolden(t, "tps_overloaded.json"))

	if got.Status != analysis.RCAStatusComplete {
		t.Errorf("status = %s, want COMPLETE", got.Status)
	}

	// The replica finding is about the VDU, so its rule names no subject and
	// concludes on the very first pass. The configuration finding is about one
	// instance, so it waits for that instance's pass and carries its path.
	want := []string{
		"rc-tps-replica-degradation",
		"rc-tps-high-log-file-config-ims.vdu_sb_logic.vnfc_sb_logic_1",
	}
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
	if restore.Code != "RESTORE_REPLICAS" || restore.MOInstance != "ims.vdu_sb_logic" ||
		restore.Op != analysis.OpReplace {
		t.Errorf("restore action = %+v", restore)
	}
	// The count is read from the snapshot rather than written into the rule, so
	// it arrives as the Go int the VDU row carries — and, more to the point, it
	// is whatever the VDU currently declares. A literal here would keep
	// recommending the old number after an operator scaled the VDU, which is
	// the same class of bug as a rule naming an instance.
	if v, ok := restore.Value.(int); !ok || v != 3 {
		t.Errorf("restore value = %#v, want int(3)", restore.Value)
	}
	if want := got.RootCauses[0].Entity; want != "ims.vdu_sb_logic" {
		t.Errorf("entity = %q, want the VDU", want)
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
