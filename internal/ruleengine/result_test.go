package ruleengine

import (
	"strings"
	"testing"

	"re/internal/analysis"
)

// assertValid is the assertion every validation test starts from, so a test
// changing one field is only testing that field.
func assertValid(r *Result) {
	r.Assert("rc-1", "CATEGORY", "summary", "ims.a", analysis.RolePrimary, 0.5)
}

func recommendValid(r *Result) {
	r.Recommend("rc-1", "RESTART_VNFC", "ims.a", analysis.OpReplace, "RESTART")
}

// --- assert validation ---------------------------------------------------

func TestAssertValidation(t *testing.T) {
	tests := []struct {
		name    string
		assert  func(r *Result)
		wantErr string
	}{
		{
			name:   "valid",
			assert: assertValid,
		},
		{
			name:    "empty id",
			assert:  func(r *Result) { r.Assert("", "C", "s", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "id is empty",
		},
		{
			name:    "blank id",
			assert:  func(r *Result) { r.Assert("   ", "C", "s", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "id is empty",
		},
		{
			name:    "empty category",
			assert:  func(r *Result) { r.Assert("rc-1", "", "s", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "category is empty",
		},
		{
			name:    "empty summary",
			assert:  func(r *Result) { r.Assert("rc-1", "C", "", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "summary is empty",
		},
		{
			name:    "empty entity",
			assert:  func(r *Result) { r.Assert("rc-1", "C", "s", "", analysis.RolePrimary, 0.5) },
			wantErr: "entity is empty",
		},
		{
			name:    "unknown role",
			assert:  func(r *Result) { r.Assert("rc-1", "C", "s", "ims.a", "BLAMED", 0.5) },
			wantErr: "role",
		},
		{
			// The scale is 0..1. An author carrying a 35 over from a
			// score-based API meant something, and it was not "certain".
			name:    "confidence above one",
			assert:  func(r *Result) { r.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 35) },
			wantErr: "confidence",
		},
		{
			name:    "negative confidence",
			assert:  func(r *Result) { r.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, -0.1) },
			wantErr: "confidence",
		},
		{
			name:   "confidence at the bounds is valid",
			assert: func(r *Result) { r.Assert("rc-1", "C", "s", "ims.a", analysis.RoleSuspected, 1) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResult()
			tc.assert(r)

			err := r.Err()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Err() = %v, want nil", err)
				}
				if len(r.RootCauses()) != 1 {
					t.Errorf("root causes = %d, want 1", len(r.RootCauses()))
				}
				return
			}
			if err == nil {
				t.Fatalf("Err() = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Err() = %q, want it to mention %q", err, tc.wantErr)
			}
			if got := len(r.RootCauses()); got != 0 {
				t.Errorf("root causes = %d, want 0 — invalid output must not be stored", got)
			}
		})
	}
}

func TestAssertNormalisesRoleCase(t *testing.T) {
	r := NewResult()
	r.Assert("rc-1", "C", "s", "ims.a", "primary", 0.5)

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if got := r.RootCauses()[0].Role; got != analysis.RolePrimary {
		t.Errorf("role = %q, want %q", got, analysis.RolePrimary)
	}
}

// --- recommend validation ------------------------------------------------

func TestRecommendValidation(t *testing.T) {
	tests := []struct {
		name      string
		recommend func(r *Result)
		wantErr   string
	}{
		{
			name:      "valid",
			recommend: recommendValid,
		},
		{
			name:      "empty root cause id",
			recommend: func(r *Result) { r.Recommend("", "C", "ims.a", analysis.OpAdd, 1) },
			wantErr:   "root cause id is empty",
		},
		{
			name:      "empty code",
			recommend: func(r *Result) { r.Recommend("rc-1", "", "ims.a", analysis.OpAdd, 1) },
			wantErr:   "code is empty",
		},
		{
			name:      "empty mo instance",
			recommend: func(r *Result) { r.Recommend("rc-1", "C", "", analysis.OpAdd, 1) },
			wantErr:   "mo instance is empty",
		},
		{
			name:      "unknown op",
			recommend: func(r *Result) { r.Recommend("rc-1", "C", "ims.a", "PATCH", 1) },
			wantErr:   "op",
		},
		{
			// Not every action takes a value. RESTART_VNFC says all it means in
			// its code, so the argument is simply absent rather than nil — which
			// GRL could not write in any case.
			name:      "no value is valid",
			recommend: func(r *Result) { r.Recommend("rc-1", "C", "ims.a", analysis.OpRemove) },
		},
		{
			// Variadic means "zero or one". Two values is a rule that has
			// misunderstood the call, and picking one would ship a change
			// nobody asked for.
			name:      "more than one value",
			recommend: func(r *Result) { r.Recommend("rc-1", "C", "ims.a", analysis.OpAdd, 1, 2) },
			wantErr:   "want at most one",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResult()
			assertValid(r)
			tc.recommend(r)

			err := r.Err()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Err() = %v, want nil", err)
				}
				if got := len(r.RootCauses()[0].Actions); got != 1 {
					t.Errorf("actions = %d, want 1", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Err() = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Err() = %q, want it to mention %q", err, tc.wantErr)
			}
			if got := len(r.RootCauses()[0].Actions); got != 0 {
				t.Errorf("actions = %d, want 0 — invalid output must not be stored", got)
			}
		})
	}
}

func TestRecommendRequiresAnAssertedCause(t *testing.T) {
	// An action whose cause was never asserted has nothing justifying it. The
	// reference has to resolve inside the same document, because a cause
	// asserted by a different one would survive that document being discarded.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-does-not-exist", "RESTART_VNFC", "ims.a", analysis.OpReplace, "RESTART")

	err := r.Err()
	if err == nil {
		t.Fatal("Err() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "was not asserted") {
		t.Errorf("Err() = %q, want it to mention the missing assertion", err)
	}
}

func TestRecommendBeforeAssertIsRejected(t *testing.T) {
	// Ordering matters within a document: the cause must exist first. Two
	// rules in one document fire in salience order, so a rule recommending
	// against a cause a lower-salience rule will assert later is a rule-set
	// bug, not something to defer.
	r := NewResult()
	recommendValid(r)
	assertValid(r)

	if r.Err() == nil {
		t.Fatal("Err() = nil, want an error")
	}
	if got := len(r.RootCauses()[0].Actions); got != 0 {
		t.Errorf("actions = %d, want 0", got)
	}
}

func TestAssertCollectsEveryError(t *testing.T) {
	// The sink does not stop at the first mistake: an operator fixing a rule
	// should see everything wrong with it, not one problem per run.
	r := NewResult()
	r.Assert("", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
	r.Assert("rc-2", "C", "s", "ims.a", "NOPE", 0.5)

	err := r.Err()
	if err == nil {
		t.Fatal("Err() = nil, want errors")
	}
	if !strings.Contains(err.Error(), "id is empty") || !strings.Contains(err.Error(), "role") {
		t.Errorf("Err() = %q, want both failures reported", err)
	}
}

// --- duplicate causes ----------------------------------------------------

func TestAssertDuplicateIsIdempotent(t *testing.T) {
	// Two rules reaching one conclusion is corroboration, not two findings.
	r := NewResult()
	assertValid(r)
	assertValid(r)

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if got := len(r.RootCauses()); got != 1 {
		t.Errorf("root causes = %d, want 1", got)
	}
}

func TestAssertConflictingMetadataIsAnError(t *testing.T) {
	tests := []struct {
		name    string
		second  func(r *Result)
		wantErr string
	}{
		{
			name:    "category",
			second:  func(r *Result) { r.Assert("rc-1", "OTHER", "summary", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "conflicting category",
		},
		{
			name:    "summary",
			second:  func(r *Result) { r.Assert("rc-1", "CATEGORY", "other", "ims.a", analysis.RolePrimary, 0.5) },
			wantErr: "conflicting summary",
		},
		{
			name:    "entity",
			second:  func(r *Result) { r.Assert("rc-1", "CATEGORY", "summary", "ims.b", analysis.RolePrimary, 0.5) },
			wantErr: "conflicting entity",
		},
		{
			name:    "role",
			second:  func(r *Result) { r.Assert("rc-1", "CATEGORY", "summary", "ims.a", analysis.RoleSuspected, 0.5) },
			wantErr: "conflicting role",
		},
		{
			// Averaging two disagreeing confidences produces a number neither
			// rule stated, so the disagreement is reported instead.
			name:    "confidence",
			second:  func(r *Result) { r.Assert("rc-1", "CATEGORY", "summary", "ims.a", analysis.RolePrimary, 0.9) },
			wantErr: "conflicting confidence",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResult()
			assertValid(r)
			tc.second(r)

			err := r.Err()
			if err == nil {
				t.Fatalf("Err() = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Err() = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// --- action deduplication ------------------------------------------------

func TestRecommendDeduplicatesIdenticalActions(t *testing.T) {
	// Several rules asking for the identical change is corroboration, exactly
	// as it is for a root cause, so it is one action rather than three.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-1", "SET_CONFIG", "ims.a", analysis.OpReplace, 3)
	r.Recommend("rc-1", "SET_CONFIG", "ims.a", analysis.OpReplace, 3)
	r.Recommend("rc-1", "SET_CONFIG", "ims.a", analysis.OpReplace, 3)

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if got := len(r.RootCauses()[0].Actions); got != 1 {
		t.Fatalf("actions = %d, want 1", got)
	}
}

func TestRecommendTreatsDifferentValuesAsDifferentActions(t *testing.T) {
	// Same field, different target value: two genuinely different proposals.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-1", "SET_CONFIG", "ims.a", analysis.OpReplace, 3)
	r.Recommend("rc-1", "SET_CONFIG", "ims.a", analysis.OpReplace, 5)

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if got := len(r.RootCauses()[0].Actions); got != 2 {
		t.Errorf("actions = %d, want 2", got)
	}
}

func TestRecommendDistinguishesEveryFingerprintField(t *testing.T) {
	base := func(r *Result) {
		r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, 1)
	}
	variants := []struct {
		name string
		fn   func(r *Result)
	}{
		{"code", func(r *Result) { r.Recommend("rc-1", "OTHER", "ims.a", analysis.OpReplace, 1) }},
		{"mo instance", func(r *Result) { r.Recommend("rc-1", "CODE", "ims.b", analysis.OpReplace, 1) }},
		{"op", func(r *Result) { r.Recommend("rc-1", "CODE", "ims.a", analysis.OpAdd, 1) }},
		{"value", func(r *Result) { r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, 2) }},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			r := NewResult()
			assertValid(r)
			base(r)
			v.fn(r)

			if err := r.Err(); err != nil {
				t.Fatalf("Err() = %v, want nil", err)
			}
			if got := len(r.RootCauses()[0].Actions); got != 2 {
				t.Errorf("actions = %d, want 2 — %s should distinguish them", got, v.name)
			}
		})
	}
}

func TestActionsWithEquivalentJSONValuesAreOneAction(t *testing.T) {
	// The fingerprint is over the canonical JSON, so a Go int and a float that
	// serialise identically are the same proposal. A rule runtime hands values
	// over as whatever type its literal parsed to, and the response carries
	// JSON either way.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, 3)
	r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, float64(3))

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if got := len(r.RootCauses()[0].Actions); got != 1 {
		t.Errorf("actions = %d, want 1", got)
	}
}

func TestRecommendRejectsUnrepresentableValue(t *testing.T) {
	// A value that cannot be JSON cannot reach a response either. Recording it
	// as an error fails the row rather than shipping an action nobody can read.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, make(chan int))

	err := r.Err()
	if err == nil {
		t.Fatal("Err() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "not representable as JSON") {
		t.Errorf("Err() = %q, want it to name the value problem", err)
	}
}

// --- ordering ------------------------------------------------------------

func TestRootCausesKeepFirstAssertionOrder(t *testing.T) {
	// Nothing ranks root causes against each other, so the only honest order
	// is the one the rules produced — and salience already made that order the
	// operator's choice.
	r := NewResult()
	r.Assert("rc-z", "C", "s", "ims.a", analysis.RolePrimary, 0.1)
	r.Assert("rc-a", "C", "s", "ims.b", analysis.RolePrimary, 0.9)
	r.Assert("rc-z", "C", "s", "ims.a", analysis.RolePrimary, 0.1) // re-assertion
	r.Assert("rc-m", "C", "s", "ims.c", analysis.RolePrimary, 0.5)

	var got []string
	for _, c := range r.RootCauses() {
		got = append(got, c.ID)
	}
	want := []string{"rc-z", "rc-a", "rc-m"}
	if len(got) != len(want) {
		t.Fatalf("root causes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("root causes = %v, want %v", got, want)
		}
	}
}

func TestActionsSortByCodeThenTieBreaks(t *testing.T) {
	r := NewResult()
	assertValid(r)
	// Declared out of order, and with a full set of ties so every sort key is
	// exercised.
	r.Recommend("rc-1", "B_CODE", "ims.a", analysis.OpReplace, 1)
	r.Recommend("rc-1", "A_CODE", "ims.b", analysis.OpReplace, 1)
	r.Recommend("rc-1", "A_CODE", "ims.a", analysis.OpReplace, 1)
	r.Recommend("rc-1", "A_CODE", "ims.a", analysis.OpAdd, 1)
	r.Recommend("rc-1", "HIGH", "ims.z", analysis.OpRemove, nil)

	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}

	actions := r.RootCauses()[0].Actions
	type key struct{ code, mo, op string }
	want := []key{
		{"A_CODE", "ims.a", analysis.OpAdd},
		{"A_CODE", "ims.a", analysis.OpReplace},
		{"A_CODE", "ims.b", analysis.OpReplace},
		{"B_CODE", "ims.a", analysis.OpReplace},
		{"HIGH", "ims.z", analysis.OpRemove},
	}
	if len(actions) != len(want) {
		t.Fatalf("actions = %d, want %d", len(actions), len(want))
	}
	for i, w := range want {
		got := key{actions[i].Code, actions[i].MOInstance, actions[i].Op}
		if got != w {
			t.Errorf("action %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestActionsWithIdenticalKeysAreOrderedByValue(t *testing.T) {
	// Two actions identical in every other field are still different
	// proposals. The value is the last sort key purely so they cannot swap
	// places between runs of a persisted, replayed response.
	r := NewResult()
	assertValid(r)
	r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, 9)
	r.Recommend("rc-1", "CODE", "ims.a", analysis.OpReplace, 1)

	actions := r.RootCauses()[0].Actions
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(actions))
	}
	if actions[0].Value != 1 || actions[1].Value != 9 {
		t.Errorf("values = %v, %v; want 1, 9", actions[0].Value, actions[1].Value)
	}
}

// --- cause set merging ---------------------------------------------------

func TestCauseSetMergeIsIdempotentAndUnionsActions(t *testing.T) {
	first := NewResult()
	first.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
	first.Recommend("rc-1", "ONE", "ims.a", analysis.OpAdd, 1)

	second := NewResult()
	second.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
	second.Recommend("rc-1", "TWO", "ims.a", analysis.OpAdd, 2)

	merged := newCauseSet()
	if err := merged.mergeFrom(first.causes); err != nil {
		t.Fatalf("merge first: %v", err)
	}
	if err := merged.mergeFrom(second.causes); err != nil {
		t.Fatalf("merge second: %v", err)
	}

	causes := merged.finalize()
	if len(causes) != 1 {
		t.Fatalf("root causes = %d, want 1", len(causes))
	}
	if got := len(causes[0].Actions); got != 2 {
		t.Errorf("actions = %d, want 2", got)
	}
}

func TestCauseSetMergeReportsConflict(t *testing.T) {
	first := NewResult()
	first.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 0.5)

	second := NewResult()
	second.Assert("rc-1", "C", "s", "ims.a", analysis.RoleContributing, 0.5)

	merged := newCauseSet()
	if err := merged.mergeFrom(first.causes); err != nil {
		t.Fatalf("merge first: %v", err)
	}
	if err := merged.mergeFrom(second.causes); err == nil {
		t.Fatal("merge second = nil, want a conflict")
	}
}

func TestCauseSetCloneIsIndependent(t *testing.T) {
	// The clone is what makes a rule row atomic, so it has to be deep: a
	// discarded row must leave nothing behind, including in the action slices.
	original := newCauseSet()
	src := NewResult()
	src.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
	src.Recommend("rc-1", "ONE", "ims.a", analysis.OpAdd, 1)
	if err := original.mergeFrom(src.causes); err != nil {
		t.Fatal(err)
	}

	clone := original.clone()

	// A second row that corroborates rc-1 with another action and adds rc-2.
	// It re-asserts rc-1 itself because a sink only accepts actions against
	// causes its own document declared.
	extra := NewResult()
	extra.Assert("rc-1", "C", "s", "ims.a", analysis.RolePrimary, 0.5)
	extra.Recommend("rc-1", "TWO", "ims.a", analysis.OpAdd, 2)
	extra.Assert("rc-2", "C", "s", "ims.b", analysis.RolePrimary, 0.5)
	if err := extra.Err(); err != nil {
		t.Fatalf("second row sink: %v", err)
	}
	if err := clone.mergeFrom(extra.causes); err != nil {
		t.Fatal(err)
	}

	if got := len(original.finalize()); got != 1 {
		t.Errorf("original root causes = %d, want 1", got)
	}
	if got := len(original.finalize()[0].Actions); got != 1 {
		t.Errorf("original actions = %d, want 1", got)
	}
	if got := len(clone.finalize()); got != 2 {
		t.Errorf("clone root causes = %d, want 2", got)
	}
	if got := len(clone.finalize()[0].Actions); got != 2 {
		t.Errorf("clone actions = %d, want 2", got)
	}
}
