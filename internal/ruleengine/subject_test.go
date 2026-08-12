package ruleengine

import (
	"context"
	"strings"
	"testing"

	"re/internal/analysis"
)

// fanOutSnapshot has two VDUs and three instances, so a row over it runs six
// passes: the no-subject one, two VDUs, three VNFCs.
func fanOutSnapshot() analysis.ContextSnapshot {
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

func TestSubjectsCoverTheSnapshotInOrder(t *testing.T) {
	got := NewFacts(fanOutSnapshot()).subjects()

	want := []struct{ kind, path string }{
		{SubjectNone, ""},
		{SubjectVDU, "ims.vdu_a"},
		{SubjectVDU, "ims.vdu_b"},
		{SubjectVNFC, "ims.vdu_a.vnfc_a_1"},
		{SubjectVNFC, "ims.vdu_a.vnfc_a_2"},
		{SubjectVNFC, "ims.vdu_b.vnfc_b_1"},
	}
	if len(got) != len(want) {
		t.Fatalf("subjects = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Kind() != w.kind || got[i].Path() != w.path {
			t.Errorf("subject %d = %s/%q, want %s/%q", i, got[i].Kind(), got[i].Path(), w.kind, w.path)
		}
	}
}

func TestSubjectsAlwaysIncludeTheNoSubjectPass(t *testing.T) {
	// A snapshot with no topology still has to run every rule once. Without
	// this pass a rule that reasons about named entities rather than about a
	// subject would never execute, and the row would report having concluded
	// nothing rather than having never been asked.
	got := NewFacts(completeSnapshot()).subjects()
	if len(got) != 1 {
		t.Fatalf("subjects = %d, want 1", len(got))
	}
	if got[0].Kind() != SubjectNone || got[0].Path() != "" {
		t.Errorf("subject = %s/%q, want the no-subject pass", got[0].Kind(), got[0].Path())
	}
}

// --- fan-out through the runner ------------------------------------------

func TestRowRunsOncePerSubject(t *testing.T) {
	rt := newScriptedRuntime()
	var seen []string
	rt.script["only"] = func(out *Result) error { return nil }
	rt.observe = func(s *SubjectFacts) { seen = append(seen, s.Kind()+":"+s.Path()) }

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("only", 100)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), fanOutSnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	want := ":,VDU:ims.vdu_a,VDU:ims.vdu_b,VNFC:ims.vdu_a.vnfc_a_1,VNFC:ims.vdu_a.vnfc_a_2,VNFC:ims.vdu_b.vnfc_b_1"
	if strings.Join(seen, ",") != want {
		t.Errorf("subjects seen = %v", seen)
	}
	if len(got.RuleExecutions) != 1 {
		t.Fatalf("executions = %d, want 1", len(got.RuleExecutions))
	}
	if p := got.RuleExecutions[0].Passes; p != 6 {
		t.Errorf("passes = %d, want 6", p)
	}
}

func TestFanOutAssertsOncePerMatchingSubject(t *testing.T) {
	// The behaviour the whole subject model exists for: one rule text, one
	// conclusion per instance that matches, without the rule naming any of
	// them.
	rt := newScriptedRuntime()
	rt.withSubject = func(out *Result, s *SubjectFacts, facts *Facts) error {
		if s.Kind() != SubjectVNFC || !facts.VNFC.IsDown(s.Path()) {
			return nil
		}
		out.Assert("rc-"+s.Path(), "C", "s", s.Path(), analysis.RolePrimary, 0.5)
		out.Recommend("rc-"+s.Path(), "RESTART_VNFC", s.Path(), analysis.OpReplace)
		return nil
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("blame", 100)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), fanOutSnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	want := []string{"rc-ims.vdu_a.vnfc_a_1", "rc-ims.vdu_a.vnfc_a_2"}
	if ids := causeIDsOf(got); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("root causes = %v, want %v", ids, want)
	}
	for _, c := range got.RootCauses {
		if len(c.Actions) != 1 || c.Actions[0].Value != nil {
			t.Errorf("%s actions = %+v", c.ID, c.Actions)
		}
	}
	if n := got.RuleExecutions[0].RootCauseCount; n != 2 {
		t.Errorf("root cause count = %d, want 2", n)
	}
}

func TestSubjectlessRuleConcludesOnceAcrossEveryPass(t *testing.T) {
	// A rule that says nothing about the subject runs on all six passes and
	// asserts the same claim each time. Re-asserting one id with the same
	// metadata is corroboration, so the row still contributes exactly one root
	// cause — which is what makes fanning out safe to switch on for rules that
	// were never written for it.
	rt := newScriptedRuntime()
	rt.script["vdu_level"] = assertOne("rc-vdu", "ims.vdu_a")

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("vdu_level", 100)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), fanOutSnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if ids := causeIDsOf(got); strings.Join(ids, ",") != "rc-vdu" {
		t.Fatalf("root causes = %v, want [rc-vdu]", ids)
	}
	if p := got.RuleExecutions[0].Passes; p != 6 {
		t.Errorf("passes = %d, want 6", p)
	}
}

func TestOnePassFailingDiscardsTheWholeRow(t *testing.T) {
	// The row stays the atomic unit. A document that gets one instance wrong is
	// a document to fix, so what it concluded about the others does not survive
	// — and the pass count says which subject it broke on.
	rt := newScriptedRuntime()
	rt.withSubject = func(out *Result, s *SubjectFacts, _ *Facts) error {
		if s.Path() == "ims.vdu_a.vnfc_a_2" {
			// Invalid output rather than a returned error: this is the path GRL
			// takes, since it has no error channel of its own.
			out.Assert("rc-bad", "C", "s", "ims.a", "NOT_A_ROLE", 0.5)
			return nil
		}
		out.Assert("rc-"+s.Path(), "C", "s", "ims.a", analysis.RolePrimary, 0.5)
		return nil
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("mixed", 100)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), fanOutSnapshot())
	if err == nil {
		t.Fatal("Analyze = nil, want the failed-row error")
	}
	if len(got.RootCauses) != 0 {
		t.Errorf("root causes = %v, want none", causeIDsOf(got))
	}

	ex := got.RuleExecutions[0]
	if ex.Status != analysis.RuleStatusFailed {
		t.Errorf("status = %s, want FAILED", ex.Status)
	}
	if !strings.Contains(ex.Error, "role") {
		t.Errorf("error = %q, want the validation failure named", ex.Error)
	}
	// Five passes ran before the bad one was checked: none, two VDUs, the first
	// VNFC, then the one that broke it.
	if ex.Passes != 5 {
		t.Errorf("passes = %d, want 5", ex.Passes)
	}
}
