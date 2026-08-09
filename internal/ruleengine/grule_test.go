package ruleengine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"re/internal/analysis"
)

// grlAssert renders a rule whose "then" asserts one root cause. Tests build
// documents out of these so the GRL under test is visible at the call site
// instead of buried in a fixture file.
func grlAssert(name string, salience int, when, causeID string) string {
	return fmt.Sprintf(`
rule %s "test rule" salience %d {
    when
        %s
    then
        Result.Assert("%s", "CATEGORY", "summary", "ims.a", "PRIMARY", 0.5);
}
`, name, salience, when, causeID)
}

func grlRule(content string) analysis.RuleDefinition {
	return analysis.RuleDefinition{ID: "rule-id", Name: "test_rule", Content: content}
}

// runGRL executes one document over an empty-but-COMPLETE snapshot.
func runGRL(t *testing.T, rt *GRLRuntime, rule analysis.RuleDefinition) (*Result, error) {
	t.Helper()
	sink := NewResult()
	err := rt.Execute(context.Background(), rule, NewFacts(completeSnapshot()), sink)
	return sink, err
}

func causeIDs(r *Result) []string {
	var out []string
	for _, c := range r.RootCauses() {
		out = append(out, c.ID)
	}
	return out
}

// --- firing ---------------------------------------------------------------

func TestGRLFiresEveryMatchingRuleOnceInSalienceOrder(t *testing.T) {
	// Grule fires one rule per cycle and re-evaluates, so without retraction
	// the first rule would be selected forever. With it, each matching rule
	// gets exactly one turn and the highest salience goes first.
	doc := grlAssert("Low", 10, "true", "rc-low") +
		grlAssert("High", 100, "true", "rc-high") +
		grlAssert("Mid", 50, "true", "rc-mid")

	sink, err := runGRL(t, NewGRLRuntime(), grlRule(doc))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := sink.Err(); err != nil {
		t.Fatalf("sink: %v", err)
	}

	got := causeIDs(sink)
	want := []string{"rc-high", "rc-mid", "rc-low"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("root causes = %v, want %v", got, want)
	}
}

func TestGRLNonMatchingRulesProduceNothing(t *testing.T) {
	doc := grlAssert("Matches", 100, `Ctx.Alerts.HasCause("PRESENT")`, "rc-yes") +
		grlAssert("DoesNot", 90, `Ctx.Alerts.HasCause("ABSENT")`, "rc-no")

	snap := completeSnapshot()
	snap.Input.Alerts = []analysis.Alert{{ID: "a", SourcePath: "ims.a", ProbableCause: "PRESENT"}}

	sink := NewResult()
	if err := NewGRLRuntime().Execute(context.Background(), grlRule(doc), NewFacts(snap), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := causeIDs(sink)
	if len(got) != 1 || got[0] != "rc-yes" {
		t.Errorf("root causes = %v, want [rc-yes]", got)
	}
}

func TestGRLRetractionDoesNotStopTheRemainingRules(t *testing.T) {
	// This is why the runtime retracts instead of calling DataContext.Complete:
	// completing ends the whole run at the first assertion, and an rca_rule row
	// is a scenario document whose later rules must still get their turn.
	const n = 6
	var doc strings.Builder
	for i := range n {
		doc.WriteString(grlAssert(fmt.Sprintf("R%d", i), 100-i, "true", fmt.Sprintf("rc-%d", i)))
	}

	sink, err := runGRL(t, NewGRLRuntime(), grlRule(doc.String()))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := len(sink.RootCauses()); got != n {
		t.Errorf("root causes = %d, want %d", got, n)
	}
}

func TestGRLRuleThatAssertsNothingTripsTheCycleGuard(t *testing.T) {
	// A rule that fires and writes nothing is never retracted, so it stays
	// runnable and is selected again. MaxCycle is what turns that into a
	// reported failure instead of a spin to the library default of 5000.
	const doc = `
rule Silent "fires and concludes nothing" salience 100 {
    when
        true
    then
        Ctx.Alerts.HasCause("ANYTHING");
}
`
	_, err := runGRL(t, NewGRLRuntime(), grlRule(doc))
	if err == nil {
		t.Fatal("Execute = nil, want a cycle-guard error")
	}
	if !strings.Contains(err.Error(), "cycles") {
		t.Errorf("Execute = %q, want it to name the cycle bound", err)
	}
}

func TestGRLDocumentWhereEveryRuleFiresStaysWithinTheCycleBound(t *testing.T) {
	// MaxCycle is exactly the rule count. Grule increments the cycle only when
	// it has something to run and breaks without incrementing when it does
	// not, so a document where all rules fire needs precisely that many — this
	// test is the boundary that would catch an off-by-one.
	for _, n := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var doc strings.Builder
			for i := range n {
				doc.WriteString(grlAssert(fmt.Sprintf("R%d", i), 100, "true", fmt.Sprintf("rc-%d", i)))
			}

			sink, err := runGRL(t, NewGRLRuntime(), grlRule(doc.String()))
			if err != nil {
				t.Fatalf("Execute with %d rules: %v", n, err)
			}
			if got := len(sink.RootCauses()); got != n {
				t.Errorf("root causes = %d, want %d", got, n)
			}
		})
	}
}

func TestGRLEvaluationErrorIsNotSilentlyANonMatch(t *testing.T) {
	// ReturnErrOnFailedRuleEvaluation is what stops a broken condition from
	// reading as "the rule did not match" — the one interpretation that turns
	// a bug into a plausible negative.
	const doc = `
rule Broken "calls a fact that does not exist" salience 100 {
    when
        Ctx.Alerts.NoSuchMethod("x")
    then
        Result.Assert("rc-1", "C", "s", "ims.a", "PRIMARY", 0.5);
}
`
	sink, err := runGRL(t, NewGRLRuntime(), grlRule(doc))
	if err == nil {
		t.Fatal("Execute = nil, want an evaluation error")
	}
	if got := len(sink.RootCauses()); got != 0 {
		t.Errorf("root causes = %d, want 0", got)
	}
}

func TestGRLOnlyCtxAndResultAreBound(t *testing.T) {
	// The binding surface is part of the rule-authoring contract: everything
	// readable is under Ctx and everything writable goes through Result.
	const doc = `
rule UsesUnbound "reads a fact name that is not bound" salience 100 {
    when
        Subject.Path == "ims.a"
    then
        Result.Assert("rc-1", "C", "s", "ims.a", "PRIMARY", 0.5);
}
`
	if _, err := runGRL(t, NewGRLRuntime(), grlRule(doc)); err == nil {
		t.Fatal("Execute = nil, want an error for an unbound fact")
	}
}

func TestGRLCancellationStopsExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	doc := grlAssert("R", 100, "true", "rc-1")
	sink := NewResult()
	err := NewGRLRuntime().Execute(ctx, grlRule(doc), NewFacts(completeSnapshot()), sink)
	if err == nil {
		t.Fatal("Execute = nil, want a cancellation error")
	}
}

// --- compilation ----------------------------------------------------------

func TestGRLCompileFailures(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "empty content",
			content: "",
			wantErr: "rule_content is empty",
		},
		{
			name:    "whitespace only",
			content: "   \n\t ",
			wantErr: "rule_content is empty",
		},
		{
			// Syntactically valid and behaviourally empty. It would otherwise
			// run, match nothing, and be reported as a rule that had nothing
			// to say.
			name:    "comments only",
			content: "// nothing here\n",
			wantErr: "compiled to no rules",
		},
		{
			name:    "malformed",
			content: "rule Broken { when then }",
			wantErr: "compile",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runGRL(t, NewGRLRuntime(), grlRule(tc.content))
			if err == nil {
				t.Fatalf("Execute = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Execute = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// --- cache ----------------------------------------------------------------

func TestCacheReusesTheCompilationForIdenticalContent(t *testing.T) {
	cache := newRuleCache()
	r := grlRule(grlAssert("R", 100, "true", "rc-1"))

	first, err := cache.get(r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.get(r)
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Error("cache returned a different compilation for identical content")
	}
}

func TestCacheReplacesTheEntryWhenContentChanges(t *testing.T) {
	// rca_rule rows are edited in place. Keying on the id alone would keep
	// executing the previous text forever after an operator changed it — the
	// one failure mode invisible from outside, because the engine keeps
	// answering, just from stale rules.
	cache := newRuleCache()

	old := analysis.RuleDefinition{ID: "same-id", Name: "r", Content: grlAssert("R", 100, "true", "rc-old")}
	first, err := cache.get(old)
	if err != nil {
		t.Fatal(err)
	}

	updated := old
	updated.Content = grlAssert("R", 100, "true", "rc-new") + grlAssert("R2", 90, "true", "rc-new-2")
	second, err := cache.get(updated)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatal("cache reused the old compilation after the content changed")
	}
	if first.hash == second.hash {
		t.Error("hashes are equal for different content")
	}
	if second.ruleCount != 2 {
		t.Errorf("ruleCount = %d, want 2", second.ruleCount)
	}
	// One entry per rule id, replaced rather than accumulated: Grule's library
	// is a map with no eviction, so a long-lived process must not keep every
	// version it has ever seen.
	if got := len(cache.items); got != 1 {
		t.Errorf("cache entries = %d, want 1", got)
	}

	// The new content is what executes now.
	sink, err := runGRL(t, NewGRLRuntime(), updated)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range causeIDs(sink) {
		if id == "rc-old" {
			t.Error("the replaced rule still executed")
		}
	}
}

func TestCacheDoesNotKeepAFailedCompilation(t *testing.T) {
	cache := newRuleCache()
	good := analysis.RuleDefinition{ID: "same-id", Name: "r", Content: grlAssert("R", 100, "true", "rc-1")}
	if _, err := cache.get(good); err != nil {
		t.Fatal(err)
	}

	broken := good
	broken.Content = "rule Broken { when then }"
	if _, err := cache.get(broken); err == nil {
		t.Fatal("get = nil, want a compile error")
	}

	// The previous compilation must not survive: a row whose new content does
	// not compile would otherwise keep answering with rules the operator has
	// already replaced.
	if cache.items[good.ID].c != nil {
		t.Error("a failed compile left the previous entry in place")
	}

	// And the failure is not cached as a success either — fixing the row works
	// on the next request.
	if _, err := cache.get(good); err != nil {
		t.Errorf("get after a failure = %v, want nil", err)
	}
}

func TestCacheIsSafeUnderConcurrentUse(t *testing.T) {
	// The engine is a server component: several requests compile and execute
	// at once. Run with -race for this to mean anything.
	cache := newRuleCache()
	rules := []analysis.RuleDefinition{
		{ID: "a", Name: "a", Content: grlAssert("A", 100, "true", "rc-a")},
		{ID: "b", Name: "b", Content: grlAssert("B", 100, "true", "rc-b")},
		{ID: "c", Name: "c", Content: grlAssert("C", 100, "true", "rc-c")},
	}

	var wg sync.WaitGroup
	errs := make([]error, 60)
	for i := range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := cache.get(rules[i%len(rules)])
			if err != nil {
				errs[i] = err
				return
			}
			if _, err := c.instance(rules[i%len(rules)].ID); err != nil {
				errs[i] = err
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := len(cache.items); got != len(rules) {
		t.Errorf("cache entries = %d, want %d", got, len(rules))
	}
}

func TestGRLRuntimeIsSafeUnderConcurrentUse(t *testing.T) {
	// Each execution takes a clone because working memory is mutable; two
	// analyses sharing one blueprint would corrupt each other's evaluation
	// state.
	rt := NewGRLRuntime()
	doc := grlRule(grlAssert("A", 100, "true", "rc-a") + grlAssert("B", 90, "true", "rc-b"))

	var wg sync.WaitGroup
	results := make([][]string, 40)
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sink := NewResult()
			if err := rt.Execute(context.Background(), doc, NewFacts(completeSnapshot()), sink); err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			results[i] = causeIDs(sink)
		}()
	}
	wg.Wait()

	for i, got := range results {
		if strings.Join(got, ",") != "rc-a,rc-b" {
			t.Fatalf("goroutine %d produced %v, want [rc-a rc-b]", i, got)
		}
	}
}
