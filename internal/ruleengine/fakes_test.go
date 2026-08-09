package ruleengine

import (
	"context"
	"errors"
	"sync"
	"time"

	"re/internal/analysis"
)

// --- repositories --------------------------------------------------------

// fakeRepo serves a fixed rule set.
type fakeRepo struct {
	rules []analysis.RuleDefinition
	err   error
}

func (r fakeRepo) LoadEnabled(context.Context) ([]analysis.RuleDefinition, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.rules, nil
}

// --- runtimes ------------------------------------------------------------

// scriptedRuntime answers per rule name from a script, so the runner and the
// status derivation can be exercised without compiling any GRL.
//
// It records the order it was called in. The runner's whole contract is that
// rows run one at a time in salience order, and an order that is only implied
// by the code is not tested by anything.
type scriptedRuntime struct {
	mu    sync.Mutex
	calls []string

	// script maps rule name to what that row should do.
	script map[string]func(out *Result) error

	// delay is applied before the script runs, used by the timeout tests.
	delay time.Duration
}

func newScriptedRuntime() *scriptedRuntime {
	return &scriptedRuntime{script: map[string]func(out *Result) error{}}
}

func (r *scriptedRuntime) Execute(
	ctx context.Context,
	rule analysis.RuleDefinition,
	_ *Facts,
	out *Result,
) error {
	r.mu.Lock()
	r.calls = append(r.calls, rule.Name)
	r.mu.Unlock()

	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	fn, ok := r.script[rule.Name]
	if !ok {
		return nil
	}
	return fn(out)
}

func (r *scriptedRuntime) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// selectiveDelayRuntime delays exactly one named row, so a test can watch a
// row exhaust its own budget while the rows behind it still run.
type selectiveDelayRuntime struct {
	*scriptedRuntime
	slow  string
	delay time.Duration
}

func (r *selectiveDelayRuntime) Execute(
	ctx context.Context,
	rule analysis.RuleDefinition,
	facts *Facts,
	out *Result,
) error {
	if rule.Name == r.slow {
		r.mu.Lock()
		r.calls = append(r.calls, rule.Name)
		r.mu.Unlock()

		select {
		case <-time.After(r.delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.scriptedRuntime.Execute(ctx, rule, facts, out)
}

// errRuntimeBroken is what a scripted row returns when the test needs it to
// fail for a reason that is not a timeout.
var errRuntimeBroken = errors.New("runtime is broken")

// --- rule definitions ----------------------------------------------------

func rule(name string, salience int) analysis.RuleDefinition {
	return analysis.RuleDefinition{
		ID:       "id-" + name,
		Name:     name,
		Content:  "rule " + name + " {}", // never compiled by the scripted runtime
		Salience: salience,
	}
}

// --- snapshots -----------------------------------------------------------

// completeSnapshot is the minimum snapshot that counts as COMPLETE, so a test
// about status derivation is not accidentally also a test about context gaps.
func completeSnapshot() analysis.ContextSnapshot {
	return analysis.ContextSnapshot{Status: analysis.StatusComplete}
}

// assertOne is the script a row uses when the test only needs it to conclude
// something.
func assertOne(id, entity string) func(out *Result) error {
	return func(out *Result) error {
		out.Assert(id, "CATEGORY", "summary", entity, analysis.RolePrimary, 0.5)
		return nil
	}
}
