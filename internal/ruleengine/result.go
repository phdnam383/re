package ruleengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"re/internal/analysis"
)

// Result is the GRL-facing output API, bound into the rule runtime under the
// name "Result". One Result serves one rca_rule row and is the row's failure
// boundary: everything a document concludes lands here first and is merged
// into the analysis only if the whole document finished cleanly.
//
// The two methods are the entire contract a rule author writes against.
// Assert opens a root cause; Recommend attaches an action to one that was
// already opened in this same document.
type Result struct {
	causes *causeSet

	// errs collects everything the rules got wrong. GRL has no error channel:
	// a method that returned an error would have nowhere to return it to, so
	// invalid output is recorded here and fails the row afterwards instead of
	// being clamped into range or dropped.
	errs []error

	// retract is installed by the runtime and removes the currently firing
	// rule from the working set. It is a callback so this file stays free of
	// any rule-library type.
	retract func()
}

// NewResult creates an empty sink for one rule document.
func NewResult() *Result {
	return &Result{causes: newCauseSet()}
}

// Assert declares a root cause.
//
// The id is chosen by the rule author and is what Recommend refers to, which is
// why it is stated rather than generated: an action and the cause it remedies
// are usually written in different rules of the same document, and a generated
// id could not be named by the second one.
//
// Confidence is on a 0..1 scale and is stated, not computed. There is no
// scoring policy left to interpret it — a rule that is less sure says a smaller
// number — and a value outside the range fails the row rather than being
// clamped, because a rule author who wrote 35.0 meant something and it was not
// "certain".
func (r *Result) Assert(id, category, summary, entity, role string, confidence float64) {
	// Retract first and unconditionally. The runtime removes a rule from the
	// working set once it has spoken; skipping that on an invalid assertion
	// would leave the rule runnable, and it would spin until the cycle guard
	// fired — reporting an exhausted cycle count instead of the validation
	// error that actually explains the failure.
	if r.retract != nil {
		r.retract()
	}

	cause := analysis.RootCause{
		ID:         strings.TrimSpace(id),
		Category:   strings.TrimSpace(category),
		Summary:    strings.TrimSpace(summary),
		Entity:     strings.TrimSpace(entity),
		Role:       strings.ToUpper(strings.TrimSpace(role)),
		Confidence: confidence,
	}
	if err := validateRootCause(cause); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	if err := r.causes.assert(cause); err != nil {
		r.errs = append(r.errs, err)
	}
}

// Recommend attaches an action to a root cause this document already asserted.
//
// The reference must resolve inside the same row. An action whose cause was
// asserted by a different document would survive that document being discarded
// and end up recommending a change with nothing left to justify it.
//
// The value is optional because not every action takes one: RESTART_VNFC says
// everything it means in its code, while SET_CONFIG needs the number to set. It
// is variadic rather than a nil argument because GRL cannot write nil — passing
// one panics inside the rule library's reflection — so "no value" has to be
// expressible as an argument that is simply not there.
func (r *Result) Recommend(rootCauseID, code, moInstance, op string, value ...any) {
	causeID := strings.TrimSpace(rootCauseID)

	// Variadic means "zero or one" here, not "any number". Two values is a rule
	// that has misunderstood the call, and guessing which one was meant would
	// ship a change nobody asked for.
	if len(value) > 1 {
		r.errs = append(r.errs, fmt.Errorf(
			"action %q: %d values given, want at most one", strings.TrimSpace(code), len(value)))
		return
	}
	var v any
	if len(value) == 1 {
		v = value[0]
	}

	action := analysis.RecommendedAction{
		Code:       strings.TrimSpace(code),
		MOInstance: strings.TrimSpace(moInstance),
		Op:         strings.ToUpper(strings.TrimSpace(op)),
		Value:      v,
	}
	if err := validateAction(causeID, action); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	if err := r.causes.recommend(causeID, action); err != nil {
		r.errs = append(r.errs, err)
	}
}

// Err reports everything the document got wrong, or nil.
func (r *Result) Err() error { return errors.Join(r.errs...) }

// RootCauses returns what this document concluded, ordered.
func (r *Result) RootCauses() []analysis.RootCause { return r.causes.finalize() }

// --- validation ----------------------------------------------------------

// Roles and operations are accepted case-insensitively and stored uppercase.
// The set of allowed values is closed either way; normalising only decides
// whether "primary" is a typo or a spelling, and canonicalising it here means
// the response never carries two spellings of one role.
var (
	validRoles = map[string]bool{
		analysis.RolePrimary:      true,
		analysis.RoleContributing: true,
		analysis.RoleSuspected:    true,
	}
	validOps = map[string]bool{
		analysis.OpAdd:     true,
		analysis.OpRemove:  true,
		analysis.OpReplace: true,
	}
)

func validateRootCause(c analysis.RootCause) error {
	switch {
	case c.ID == "":
		return errors.New("root cause: id is empty")
	case c.Category == "":
		return fmt.Errorf("root cause %q: category is empty", c.ID)
	case c.Summary == "":
		return fmt.Errorf("root cause %q: summary is empty", c.ID)
	case c.Entity == "":
		return fmt.Errorf("root cause %q: entity is empty", c.ID)
	case !validRoles[c.Role]:
		return fmt.Errorf("root cause %q: role %q is not PRIMARY, CONTRIBUTING or SUSPECTED", c.ID, c.Role)
	case c.Confidence < 0 || c.Confidence > 1:
		return fmt.Errorf("root cause %q: confidence %v is outside 0..1", c.ID, c.Confidence)
	}
	return nil
}

func validateAction(causeID string, a analysis.RecommendedAction) error {
	switch {
	case causeID == "":
		return fmt.Errorf("action %q: root cause id is empty", a.Code)
	case a.Code == "":
		return fmt.Errorf("action for root cause %q: code is empty", causeID)
	case a.MOInstance == "":
		return fmt.Errorf("action %q: mo instance is empty", a.Code)
	case !validOps[a.Op]:
		return fmt.Errorf("action %q: op %q is not ADD, REMOVE or REPLACE", a.Code, a.Op)
	}
	return nil
}

// --- cause set -----------------------------------------------------------

// causeSet accumulates root causes and their actions, deduplicating both.
//
// The same structure serves two levels: one per rule document while it runs,
// and one for the whole analysis that finished documents are folded into. That
// is deliberate — the rules for "two rules said the same thing" and "two
// documents said the same thing" are identical, and stating them twice is how
// they drift apart.
type causeSet struct {
	// order holds ids in first-assertion order. Root causes are not ranked:
	// nothing here scores them against each other, so the only honest order is
	// the one the rules produced, and salience already made that order the
	// operator's choice.
	order []string
	byID  map[string]*causeEntry
}

type causeEntry struct {
	cause analysis.RootCause

	// seen holds the fingerprint of every action already attached, so a repeat
	// is recognised without rescanning the slice.
	seen map[string]bool
}

func newCauseSet() *causeSet {
	return &causeSet{byID: map[string]*causeEntry{}}
}

// clone deep-copies the set so a merge can be attempted and thrown away.
// That is what makes a row atomic: a document whose third rule conflicts must
// leave nothing behind from its first two.
func (s *causeSet) clone() *causeSet {
	out := &causeSet{
		order: append([]string(nil), s.order...),
		byID:  make(map[string]*causeEntry, len(s.byID)),
	}
	for id, e := range s.byID {
		c := e.cause
		c.Actions = append([]analysis.RecommendedAction(nil), e.cause.Actions...)
		seen := make(map[string]bool, len(e.seen))
		for k := range e.seen {
			seen[k] = true
		}
		out.byID[id] = &causeEntry{cause: c, seen: seen}
	}
	return out
}

// assert adds a root cause, or reconciles it with one already present.
//
// Re-asserting the same id with the same metadata is idempotent: two rules
// reaching one conclusion is corroboration, not two findings. Re-asserting it
// with different metadata is a contradiction — one id cannot name two
// different claims — and is reported rather than resolved, because there is no
// principled way to pick a winner and averaging two disagreeing confidences
// produces a number neither rule stated.
func (s *causeSet) assert(c analysis.RootCause) error {
	existing, ok := s.byID[c.ID]
	if !ok {
		entry := &causeEntry{cause: c, seen: map[string]bool{}}
		entry.cause.Actions = nil
		s.byID[c.ID] = entry
		s.order = append(s.order, c.ID)
		return nil
	}
	if err := sameClaim(existing.cause, c); err != nil {
		return err
	}
	return nil
}

// sameClaim reports whether two assertions of one id say the same thing.
func sameClaim(a, b analysis.RootCause) error {
	switch {
	case a.Category != b.Category:
		return fmt.Errorf("root cause %q: conflicting category %q and %q", a.ID, a.Category, b.Category)
	case a.Summary != b.Summary:
		return fmt.Errorf("root cause %q: conflicting summary %q and %q", a.ID, a.Summary, b.Summary)
	case a.Entity != b.Entity:
		return fmt.Errorf("root cause %q: conflicting entity %q and %q", a.ID, a.Entity, b.Entity)
	case a.Role != b.Role:
		return fmt.Errorf("root cause %q: conflicting role %q and %q", a.ID, a.Role, b.Role)
	case a.Confidence != b.Confidence:
		return fmt.Errorf("root cause %q: conflicting confidence %v and %v", a.ID, a.Confidence, b.Confidence)
	}
	return nil
}

// recommend attaches an action to an already-asserted cause.
func (s *causeSet) recommend(causeID string, a analysis.RecommendedAction) error {
	entry, ok := s.byID[causeID]
	if !ok {
		return fmt.Errorf("action %q: root cause %q was not asserted", a.Code, causeID)
	}

	fp, err := actionFingerprint(a)
	if err != nil {
		return err
	}
	if entry.seen[fp] {
		// Same change requested twice. Two rules proposing the identical
		// change is corroboration, exactly as it is for a root cause, so the
		// repeat is absorbed rather than listed again.
		return nil
	}
	entry.seen[fp] = true
	entry.cause.Actions = append(entry.cause.Actions, a)
	return nil
}

// mergeFrom folds another set into this one under the same rules.
func (s *causeSet) mergeFrom(other *causeSet) error {
	for _, id := range other.order {
		e := other.byID[id]
		if err := s.assert(e.cause); err != nil {
			return err
		}
		for _, a := range e.cause.Actions {
			if err := s.recommend(id, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// finalize returns the ordered root causes with their actions sorted.
func (s *causeSet) finalize() []analysis.RootCause {
	out := make([]analysis.RootCause, 0, len(s.order))
	for _, id := range s.order {
		c := s.byID[id].cause
		c.Actions = sortActions(c.Actions)
		out = append(out, c)
	}
	return out
}

// sortActions orders actions by code, then managed object, operation and
// value.
//
// Nothing ranks actions against each other, so the order exists purely for
// determinism: the response is persisted and replayed, and two actions must
// not swap places between runs. The value is the last key because two actions
// can be identical in every other field and still be different proposals.
func sortActions(actions []analysis.RecommendedAction) []analysis.RecommendedAction {
	if len(actions) < 2 {
		return actions
	}
	out := append([]analysis.RecommendedAction(nil), actions...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.MOInstance != b.MOInstance {
			return a.MOInstance < b.MOInstance
		}
		if a.Op != b.Op {
			return a.Op < b.Op
		}
		return canonicalValue(a.Value) < canonicalValue(b.Value)
	})
	return out
}

// actionFingerprint identifies the change an action proposes. Every field of
// an action takes part: what is left is exactly what is proposed, so two
// actions with the same fingerprint are the same proposal.
func actionFingerprint(a analysis.RecommendedAction) (string, error) {
	value, err := json.Marshal(a.Value)
	if err != nil {
		return "", fmt.Errorf("action %q: value is not representable as JSON: %w", a.Code, err)
	}
	return strings.Join([]string{a.Code, a.MOInstance, a.Op, string(value)}, "\x00"), nil
}

// canonicalValue renders a value for ordering only. An unmarshalable value
// cannot reach here — actionFingerprint rejected it before the action was
// stored.
func canonicalValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
