package ruleengine

import (
	"context"
	"testing"

	"re/internal/analysis"
)

func topologySnapshot() analysis.ContextSnapshot {
	return analysis.ContextSnapshot{
		Status: analysis.StatusComplete,
		VDUs: []analysis.VDU{
			{Path: "ims.vdu_a", Replicas: 2},
			{Path: "ims.vdu_b", Replicas: 1},
		},
		VNFCs: []analysis.VNFC{
			{Path: "ims.vdu_a.vnfc_a_1", VDUPath: "ims.vdu_a", Status: "TERMINATED"},
			{Path: "ims.vdu_a.vnfc_a_2", VDUPath: "ims.vdu_a", Status: "TERMINATED"},
			{Path: "ims.vdu_b.vnfc_b_1", VDUPath: "ims.vdu_b", Status: "RUNNING"},
		},
	}
}

func TestRowRunsExactlyOnceRegardlessOfTopologySize(t *testing.T) {
	rt := newScriptedRuntime()
	runs := 0
	rt.script["only"] = func(*Result) error {
		runs++
		return nil
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("only", 100)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), topologySnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if runs != 1 {
		t.Errorf("runs = %d, want 1", runs)
	}
	if len(got.RuleExecutions) != 1 {
		t.Fatalf("executions = %d, want 1", len(got.RuleExecutions))
	}
	if passes := got.RuleExecutions[0].Passes; passes != 1 {
		t.Errorf("passes = %d, want 1", passes)
	}
}

func TestRowWithNoTopologyStillRunsExactlyOnce(t *testing.T) {
	rt := newScriptedRuntime()
	runs := 0
	rt.script["only"] = func(*Result) error {
		runs++
		return nil
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("only", 100)}},
		Runtime: rt,
	})

	if _, err := e.Analyze(context.Background(), completeSnapshot()); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if runs != 1 {
		t.Errorf("runs = %d, want 1", runs)
	}
}
