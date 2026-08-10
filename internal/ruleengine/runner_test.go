package ruleengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"re/internal/analysis"
)

func newEngine(t *testing.T, opts Options) *Engine {
	t.Helper()
	e, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func executionByName(t *testing.T, result analysis.RCAResult, name string) analysis.RuleExecution {
	t.Helper()
	for _, ex := range result.RuleExecutions {
		if ex.RuleName == name {
			return ex
		}
	}
	t.Fatalf("no execution record for %q", name)
	return analysis.RuleExecution{}
}

// --- construction ---------------------------------------------------------

func TestNewRequiresARepository(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New = nil, want an error")
	}
}

func TestNewDefaultsToTheGRLRuntime(t *testing.T) {
	e := newEngine(t, Options{Rules: fakeRepo{}})
	if _, ok := e.runtime.(*GRLRuntime); !ok {
		t.Errorf("runtime = %T, want *GRLRuntime", e.runtime)
	}
	if e.timeout != DefaultRuleTimeout {
		t.Errorf("timeout = %v, want %v", e.timeout, DefaultRuleTimeout)
	}
}

// --- ordering -------------------------------------------------------------

func TestRowsRunSequentiallyInSalienceThenNameOrder(t *testing.T) {
	// Salience is the operator's statement of what to consider first, and the
	// tie-break on name is what keeps two rules of equal salience from
	// swapping places between runs.
	rt := newScriptedRuntime()
	rules := []analysis.RuleDefinition{
		rule("zebra", 50),
		rule("alpha", 50),
		rule("lowest", 10),
		rule("highest", 100),
		rule("middle", 50),
	}

	e := newEngine(t, Options{Rules: fakeRepo{rules: rules}, Runtime: rt})
	if _, err := e.Analyze(context.Background(), completeSnapshot()); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	got := strings.Join(rt.called(), ",")
	want := "highest,alpha,middle,zebra,lowest"
	if got != want {
		t.Errorf("execution order = %s, want %s", got, want)
	}
}

func TestExecutionRecordsFollowExecutionOrder(t *testing.T) {
	rt := newScriptedRuntime()
	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("b", 10), rule("a", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got.RuleExecutions) != 2 {
		t.Fatalf("executions = %d, want 2", len(got.RuleExecutions))
	}
	if got.RuleExecutions[0].RuleName != "a" || got.RuleExecutions[1].RuleName != "b" {
		t.Errorf("executions = %v, want [a b]", got.RuleExecutions)
	}
}

// --- failure isolation ----------------------------------------------------

func TestOneFailingRowDoesNotEraseTheOthers(t *testing.T) {
	rt := newScriptedRuntime()
	rt.script["broken"] = func(*Result) error { return errRuntimeBroken }
	rt.script["good"] = assertOne("rc-good", "ims.a")

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("broken", 100), rule("good", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	// One broken row among several that worked is a partial answer, not a
	// failed request.
	if err != nil {
		t.Fatalf("Analyze = %v, want nil", err)
	}
	if got.Status != analysis.RCAStatusPartial {
		t.Errorf("status = %s, want PARTIAL", got.Status)
	}
	if len(got.RootCauses) != 1 || got.RootCauses[0].ID != "rc-good" {
		t.Errorf("root causes = %v, want [rc-good]", got.RootCauses)
	}

	broken := executionByName(t, got, "broken")
	if broken.Status != analysis.RuleStatusFailed {
		t.Errorf("broken status = %s, want FAILED", broken.Status)
	}
	if !strings.Contains(broken.Error, errRuntimeBroken.Error()) {
		t.Errorf("broken error = %q, want it to carry the runtime failure", broken.Error)
	}
}

func TestAPanickingRowFailsOnlyItself(t *testing.T) {
	// Rule content comes from the database. A document that makes the runtime
	// panic must fail its own row, never the process.
	rt := newScriptedRuntime()
	rt.script["panics"] = func(*Result) error { panic("boom") }
	rt.script["good"] = assertOne("rc-good", "ims.a")

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("panics", 100), rule("good", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze = %v, want nil", err)
	}
	if len(got.RootCauses) != 1 {
		t.Errorf("root causes = %d, want 1", len(got.RootCauses))
	}
	if ex := executionByName(t, got, "panics"); !strings.Contains(ex.Error, "boom") {
		t.Errorf("error = %q, want it to carry the panic value", ex.Error)
	}
}

func TestInvalidOutputFailsTheRowEvenWhenTheRuntimeSucceeds(t *testing.T) {
	// GRL has no error channel, so a rule that stated a confidence of 35 or
	// named a cause it never asserted reports it through the sink.
	rt := newScriptedRuntime()
	rt.script["invalid"] = func(out *Result) error {
		out.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 35)
		return nil
	}
	rt.script["good"] = assertOne("rc-good", "ims.a")

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("invalid", 100), rule("good", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze = %v, want nil", err)
	}
	if ex := executionByName(t, got, "invalid"); ex.Status != analysis.RuleStatusFailed {
		t.Errorf("status = %s, want FAILED", ex.Status)
	}
	if len(got.RootCauses) != 1 || got.RootCauses[0].ID != "rc-good" {
		t.Errorf("root causes = %v, want only rc-good", got.RootCauses)
	}
}

// --- atomicity ------------------------------------------------------------

func TestAFailingRowDiscardsEverythingItAsserted(t *testing.T) {
	// The row is the failure boundary. A document that asserted two causes and
	// then broke must leave neither behind — a half-applied scenario is not a
	// smaller conclusion, it is a wrong one.
	rt := newScriptedRuntime()
	rt.script["partial"] = func(out *Result) error {
		out.Assert("rc-a", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
		out.Assert("rc-b", "C", "s", "ims.b", analysis.RolePrimary, 0.5)
		return errRuntimeBroken
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("partial", 100)}},
		Runtime: rt,
	})

	got, _ := e.Analyze(context.Background(), completeSnapshot())
	if len(got.RootCauses) != 0 {
		t.Errorf("root causes = %v, want none", got.RootCauses)
	}
	if ex := executionByName(t, got, "partial"); ex.RootCauseCount != 0 {
		t.Errorf("root cause count = %d, want 0", ex.RootCauseCount)
	}
}

func TestAConflictingRowIsDiscardedWhole(t *testing.T) {
	// The conflict is only discoverable at merge time, which is why the merge
	// is attempted against a copy.
	rt := newScriptedRuntime()
	rt.script["first"] = func(out *Result) error {
		out.Assert("rc-shared", "C", "summary", "ims.a", analysis.RolePrimary, 0.5)
		return nil
	}
	rt.script["second"] = func(out *Result) error {
		out.Assert("rc-new", "C", "s", "ims.z", analysis.RolePrimary, 0.5)
		// Same id, different claim.
		out.Assert("rc-shared", "C", "a different summary", "ims.a", analysis.RolePrimary, 0.5)
		return nil
	}

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("first", 100), rule("second", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze = %v, want nil", err)
	}
	if got.Status != analysis.RCAStatusPartial {
		t.Errorf("status = %s, want PARTIAL", got.Status)
	}

	// The first row keeps its conclusion; the second loses everything,
	// including rc-new, which conflicted with nothing.
	if len(got.RootCauses) != 1 || got.RootCauses[0].ID != "rc-shared" {
		t.Fatalf("root causes = %v, want only rc-shared", got.RootCauses)
	}
	if got.RootCauses[0].Summary != "summary" {
		t.Errorf("summary = %q, want the first row's claim untouched", got.RootCauses[0].Summary)
	}

	second := executionByName(t, got, "second")
	if second.Status != analysis.RuleStatusFailed {
		t.Errorf("second status = %s, want FAILED", second.Status)
	}
	if !strings.Contains(second.Error, "conflicting summary") {
		t.Errorf("second error = %q, want the conflict named", second.Error)
	}
}

func TestCorroboratingRowsUnionTheirActions(t *testing.T) {
	rt := newScriptedRuntime()
	same := func(code string) func(*Result) error {
		return func(out *Result) error {
			out.Assert("rc-shared", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
			out.Recommend("rc-shared", code, "ims.a", analysis.OpReplace, 1)
			return nil
		}
	}
	rt.script["first"] = same("ONE")
	rt.script["second"] = same("TWO")

	e := newEngine(t, Options{
		Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("first", 100), rule("second", 90)}},
		Runtime: rt,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got.RootCauses) != 1 {
		t.Fatalf("root causes = %d, want 1", len(got.RootCauses))
	}
	if n := len(got.RootCauses[0].Actions); n != 2 {
		t.Errorf("actions = %d, want 2", n)
	}
	// Each row still reports what it asserted, even though they merged into one.
	if ex := executionByName(t, got, "second"); ex.RootCauseCount != 1 {
		t.Errorf("second root cause count = %d, want 1", ex.RootCauseCount)
	}
}

// --- timeouts and cancellation --------------------------------------------

func TestPerRowTimeoutFailsOnlyTheSlowRow(t *testing.T) {
	rt := newScriptedRuntime()
	rt.delay = 50 * time.Millisecond

	e := newEngine(t, Options{
		Rules:       fakeRepo{rules: []analysis.RuleDefinition{rule("slow", 100), rule("also_slow", 90)}},
		Runtime:     rt,
		RuleTimeout: 5 * time.Millisecond,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	// Both rows time out on their own budget, so nothing ran and that is a
	// failed analysis — but each was still started.
	if err == nil {
		t.Fatal("Analyze = nil, want an error")
	}
	if got.Status != analysis.RCAStatusFailed {
		t.Errorf("status = %s, want FAILED", got.Status)
	}
	if len(rt.called()) != 2 {
		t.Errorf("rows started = %d, want 2 — a row timeout must not stop the run", len(rt.called()))
	}
	for _, ex := range got.RuleExecutions {
		if ex.Status != analysis.RuleStatusFailed {
			t.Errorf("%s status = %s, want FAILED", ex.RuleName, ex.Status)
		}
	}
}

func TestARowTimeoutDoesNotStopTheRowsBehindIt(t *testing.T) {
	rt := &selectiveDelayRuntime{
		scriptedRuntime: newScriptedRuntime(),
		slow:            "slow",
		delay:           50 * time.Millisecond,
	}
	rt.script["fast"] = assertOne("rc-fast", "ims.a")

	e := newEngine(t, Options{
		Rules:       fakeRepo{rules: []analysis.RuleDefinition{rule("slow", 100), rule("fast", 90)}},
		Runtime:     rt,
		RuleTimeout: 5 * time.Millisecond,
	})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if err != nil {
		t.Fatalf("Analyze = %v, want nil", err)
	}
	if len(got.RootCauses) != 1 || got.RootCauses[0].ID != "rc-fast" {
		t.Errorf("root causes = %v, want [rc-fast]", got.RootCauses)
	}
	if ex := executionByName(t, got, "slow"); ex.Status != analysis.RuleStatusFailed {
		t.Errorf("slow status = %s, want FAILED", ex.Status)
	}
}

func TestAShorterRequestDeadlineWins(t *testing.T) {
	// A row budget cannot buy time the caller is no longer waiting for.
	rt := newScriptedRuntime()
	rt.delay = time.Hour

	e := newEngine(t, Options{
		Rules:       fakeRepo{rules: []analysis.RuleDefinition{rule("slow", 100)}},
		Runtime:     rt,
		RuleTimeout: time.Hour,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := e.Analyze(ctx, completeSnapshot()); err == nil {
		t.Fatal("Analyze = nil, want an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Analyze took %v; the request deadline did not win", elapsed)
	}
}

func TestCancellationMarksTheRemainingRowsSkipped(t *testing.T) {
	// A row that cannot complete should not be started, and the rows behind it
	// are reported for a stated reason rather than disappearing — silence from
	// a rule that ran and silence from one that never ran are different facts.
	rt := newScriptedRuntime()
	ctx, cancel := context.WithCancel(context.Background())

	rt.script["first"] = func(out *Result) error {
		out.Assert("rc-first", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
		cancel()
		return nil
	}

	e := newEngine(t, Options{
		Rules: fakeRepo{rules: []analysis.RuleDefinition{
			rule("first", 100), rule("second", 90), rule("third", 80),
		}},
		Runtime: rt,
	})

	got, err := e.Analyze(ctx, completeSnapshot())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Analyze = %v, want context.Canceled", err)
	}
	if len(rt.called()) != 1 {
		t.Errorf("rows started = %v, want only the first", rt.called())
	}

	for _, name := range []string{"second", "third"} {
		ex := executionByName(t, got, name)
		if ex.Status != analysis.RuleStatusSkipped {
			t.Errorf("%s status = %s, want SKIPPED", name, ex.Status)
		}
		if ex.Error == "" {
			t.Errorf("%s has no reason recorded", name)
		}
	}

	// What did finish is still reported: the caller gets a truncated answer,
	// not an empty one.
	if len(got.RootCauses) != 1 {
		t.Errorf("root causes = %d, want 1", len(got.RootCauses))
	}
	if got.Status != analysis.RCAStatusPartial {
		t.Errorf("status = %s, want PARTIAL", got.Status)
	}
}

func TestAnAlreadyCancelledRequestRunsNothing(t *testing.T) {
	rt := newScriptedRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := newEngine(t, Options{Rules: fakeRepo{rules: []analysis.RuleDefinition{rule("a", 100)}}, Runtime: rt})

	got, err := e.Analyze(ctx, completeSnapshot())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Analyze = %v, want context.Canceled", err)
	}
	if got.Status != analysis.RCAStatusFailed {
		t.Errorf("status = %s, want FAILED", got.Status)
	}
	if len(rt.called()) != 0 {
		t.Errorf("rows started = %v, want none", rt.called())
	}
}

// --- status derivation ----------------------------------------------------

func TestStatusDerivation(t *testing.T) {
	tests := []struct {
		name          string
		contextStatus string
		script        map[string]func(*Result) error
		rules         []analysis.RuleDefinition
		wantStatus    string
		wantErr       bool
	}{
		{
			name:          "complete",
			contextStatus: analysis.StatusComplete,
			rules:         []analysis.RuleDefinition{rule("a", 100)},
			script:        map[string]func(*Result) error{"a": assertOne("rc-1", "ims.a")},
			wantStatus:    analysis.RCAStatusComplete,
		},
		{
			// Everything ran and the rule set had nothing to say. That is an
			// answer about the rule set, not a failure.
			name:          "no conclusion",
			contextStatus: analysis.StatusComplete,
			rules:         []analysis.RuleDefinition{rule("a", 100)},
			wantStatus:    analysis.RCAStatusNoConclusion,
		},
		{
			// A conclusion reached without the full picture cannot claim to be
			// complete, even though every rule ran.
			name:          "partial context with causes",
			contextStatus: analysis.StatusPartial,
			rules:         []analysis.RuleDefinition{rule("a", 100)},
			script:        map[string]func(*Result) error{"a": assertOne("rc-1", "ims.a")},
			wantStatus:    analysis.RCAStatusPartial,
		},
		{
			name:          "partial context without causes",
			contextStatus: analysis.StatusPartial,
			rules:         []analysis.RuleDefinition{rule("a", 100)},
			wantStatus:    analysis.RCAStatusPartial,
		},
		{
			name:          "one row failed",
			contextStatus: analysis.StatusComplete,
			rules:         []analysis.RuleDefinition{rule("a", 100), rule("b", 90)},
			script: map[string]func(*Result) error{
				"a": assertOne("rc-1", "ims.a"),
				"b": func(*Result) error { return errRuntimeBroken },
			},
			wantStatus: analysis.RCAStatusPartial,
		},
		{
			// Nothing executed, so the absence of root causes says nothing
			// about the incident.
			name:          "every row failed",
			contextStatus: analysis.StatusComplete,
			rules:         []analysis.RuleDefinition{rule("a", 100), rule("b", 90)},
			script: map[string]func(*Result) error{
				"a": func(*Result) error { return errRuntimeBroken },
				"b": func(*Result) error { return errRuntimeBroken },
			},
			wantStatus: analysis.RCAStatusFailed,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := newScriptedRuntime()
			for name, fn := range tc.script {
				rt.script[name] = fn
			}

			e := newEngine(t, Options{Rules: fakeRepo{rules: tc.rules}, Runtime: rt})
			got, err := e.Analyze(context.Background(), analysis.ContextSnapshot{Status: tc.contextStatus})

			if tc.wantErr != (err != nil) {
				t.Fatalf("Analyze error = %v, want error: %v", err, tc.wantErr)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tc.wantStatus)
			}
		})
	}
}

func TestNoEnabledRulesIsAnError(t *testing.T) {
	// "No rule matched" is an answer about the incident; "there are no rules"
	// is an answer about the engine's own configuration. Reporting the second
	// as the first would let an empty rule table look like a healthy system.
	e := newEngine(t, Options{Rules: fakeRepo{}})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if !errors.Is(err, ErrRCARuleNotFound) {
		t.Fatalf("Analyze = %v, want ErrRCARuleNotFound", err)
	}
	if got.Status != analysis.RCAStatusFailed {
		t.Errorf("status = %s, want FAILED", got.Status)
	}
}

func TestRepositoryFailureIsAnError(t *testing.T) {
	repoErr := errors.New("connection refused")
	e := newEngine(t, Options{Rules: fakeRepo{err: repoErr}})

	got, err := e.Analyze(context.Background(), completeSnapshot())
	if !errors.Is(err, repoErr) {
		t.Fatalf("Analyze = %v, want the repository failure", err)
	}
	if got.Status != analysis.RCAStatusFailed {
		t.Errorf("status = %s, want FAILED", got.Status)
	}
	if len(got.RuleExecutions) != 0 {
		t.Errorf("executions = %d, want none — nothing ran", len(got.RuleExecutions))
	}
}

// --- determinism ----------------------------------------------------------

func TestAnalyzeIsDeterministic(t *testing.T) {
	makeEngine := func() *Engine {
		rt := newScriptedRuntime()
		rt.script["a"] = func(out *Result) error {
			out.Assert("rc-a", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
			out.Recommend("rc-a", "Z_CODE", "ims.a", analysis.OpReplace, 1)
			out.Recommend("rc-a", "A_CODE", "ims.a", analysis.OpReplace, 1)
			out.Recommend("rc-a", "HIGH", "ims.a", analysis.OpAdd, 2)
			return nil
		}
		rt.script["b"] = assertOne("rc-b", "ims.b")
		return newEngine(t, Options{
			Rules:   fakeRepo{rules: []analysis.RuleDefinition{rule("b", 50), rule("a", 50)}},
			Runtime: rt,
		})
	}

	var first string
	for i := range 20 {
		got, err := makeEngine().Analyze(context.Background(), completeSnapshot())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var sb strings.Builder
		for _, c := range got.RootCauses {
			sb.WriteString(c.ID)
			for _, a := range c.Actions {
				sb.WriteString("|" + a.Code)
			}
			sb.WriteString(";")
		}
		if i == 0 {
			first = sb.String()
			continue
		}
		if sb.String() != first {
			t.Fatalf("run %d produced %s, want %s", i, sb.String(), first)
		}
	}
	if first != "rc-a|A_CODE|HIGH|Z_CODE;rc-b;" {
		t.Errorf("output = %s", first)
	}
}
