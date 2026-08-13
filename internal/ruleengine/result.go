package ruleengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"re/internal/analysis"
)

// Result is the GRL-facing output accumulator for one rca_rule document.
// Assert selects a root cause for the currently firing GRL rule; a specialised
// Recommend method then adds components and actions to that assertion.
type Result struct {
	causes *causeSet
	errs   []error

	// Both callbacks are installed by the GRL runtime for the duration of one
	// execution. currentRule scopes Assert/Recommend association so one GRL
	// rule can never attach an action to another rule's assertion.
	retract      func()
	currentRule  func() string
	activeByRule map[string]rootCauseKey
}

func NewResult() *Result {
	return &Result{
		causes:       newCauseSet(),
		activeByRule: make(map[string]rootCauseKey),
	}
}

// Assert creates or selects a root cause. Identity is the complete public
// claim: category + role + summary.
func (r *Result) Assert(category, role, summary string) {
	if r.retract != nil {
		r.retract()
	}

	cause := analysis.RootCause{
		Category: strings.TrimSpace(category),
		Role:     strings.ToUpper(strings.TrimSpace(role)),
		Summary:  strings.TrimSpace(summary),
	}
	if err := validateRootCause(cause); err != nil {
		r.errs = append(r.errs, err)
		return
	}

	key := keyOf(cause)
	r.causes.assert(cause)
	r.activeByRule[r.ruleScope()] = key
}

// RecommendRestartVNFC adds one component per terminated VNFC path and gives
// each component the standard restart action.
func (r *Result) RecommendRestartVNFC(paths []string) {
	key, ok := r.activeCause("RESTART_VNFC")
	if !ok {
		return
	}
	if len(paths) == 0 {
		r.errs = append(r.errs, errors.New("RESTART_VNFC: no VNFC paths"))
		return
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		component := analysis.Component{
			Entity: path,
			Action: &analysis.RecommendedAction{
				Code:       "RESTART_VNFC",
				MOInstance: path,
				Op:         analysis.OpReplace,
			},
		}
		if err := r.causes.addComponent(key, component); err != nil {
			r.errs = append(r.errs, err)
		}
	}
}

// RecommendSetConfig adds a configuration action for one component. Entity
// identifies the affected component, while the managed-object instance fully
// identifies the setting being changed. Value carries only the replacement
// value.
func (r *Result) RecommendSetConfig(entity, moInstance string, value any) {
	key, ok := r.activeCause("SET_CONFIG")
	if !ok {
		return
	}
	entity = strings.TrimSpace(entity)
	moInstance = strings.TrimSpace(moInstance)
	component := analysis.Component{
		Entity: entity,
		Action: &analysis.RecommendedAction{
			Code:       "SET_CONFIG",
			MOInstance: moInstance,
			Op:         analysis.OpReplace,
			Value:      value,
		},
	}
	if err := r.causes.addComponent(key, component); err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *Result) ruleScope() string {
	if r.currentRule == nil {
		return ""
	}
	return r.currentRule()
}

func (r *Result) activeCause(action string) (rootCauseKey, bool) {
	key, ok := r.activeByRule[r.ruleScope()]
	if !ok {
		r.errs = append(r.errs, fmt.Errorf("%s: current rule has no successful Assert", action))
	}
	return key, ok
}

func (r *Result) Err() error { return errors.Join(r.errs...) }

func (r *Result) RootCauses() []analysis.RootCause { return r.causes.finalize() }

var validRoles = map[string]bool{
	analysis.RolePrimary:      true,
	analysis.RoleContributing: true,
	analysis.RoleSuspected:    true,
}

var validOps = map[string]bool{
	analysis.OpAdd:     true,
	analysis.OpRemove:  true,
	analysis.OpReplace: true,
}

func validateRootCause(c analysis.RootCause) error {
	switch {
	case c.Category == "":
		return errors.New("root cause: category is empty")
	case !validRoles[c.Role]:
		return fmt.Errorf("root cause %q: role %q is not PRIMARY, CONTRIBUTING or SUSPECTED", c.Category, c.Role)
	case c.Summary == "":
		return fmt.Errorf("root cause %q: summary is empty", c.Category)
	}
	return nil
}

func validateComponent(c analysis.Component) error {
	if strings.TrimSpace(c.Entity) == "" {
		return errors.New("component: entity is empty")
	}
	if c.Action == nil {
		return nil
	}
	a := c.Action
	switch {
	case strings.TrimSpace(a.Code) == "":
		return fmt.Errorf("component %q: action code is empty", c.Entity)
	case strings.TrimSpace(a.MOInstance) == "":
		return fmt.Errorf("action %q: mo instance is empty", a.Code)
	case !validOps[a.Op]:
		return fmt.Errorf("action %q: op %q is not ADD, REMOVE or REPLACE", a.Code, a.Op)
	}
	if _, err := json.Marshal(a.Value); err != nil {
		return fmt.Errorf("action %q: value is not representable as JSON: %w", a.Code, err)
	}
	return nil
}

type rootCauseKey struct {
	category string
	role     string
	summary  string
}

func keyOf(c analysis.RootCause) rootCauseKey {
	return rootCauseKey{category: c.Category, role: c.Role, summary: c.Summary}
}

type causeSet struct {
	order []rootCauseKey
	byKey map[rootCauseKey]*analysis.RootCause
}

func newCauseSet() *causeSet {
	return &causeSet{byKey: make(map[rootCauseKey]*analysis.RootCause)}
}

func (s *causeSet) clone() *causeSet {
	out := newCauseSet()
	out.order = append(out.order, s.order...)
	for key, cause := range s.byKey {
		copyCause := *cause
		copyCause.Components = cloneComponents(cause.Components)
		out.byKey[key] = &copyCause
	}
	return out
}

func cloneComponents(in []analysis.Component) []analysis.Component {
	out := make([]analysis.Component, len(in))
	for i, component := range in {
		out[i] = component
		if component.Action != nil {
			action := *component.Action
			out[i].Action = &action
		}
	}
	return out
}

func (s *causeSet) assert(cause analysis.RootCause) rootCauseKey {
	key := keyOf(cause)
	if _, ok := s.byKey[key]; ok {
		return key
	}
	cause.Components = nil
	s.byKey[key] = &cause
	s.order = append(s.order, key)
	return key
}

func (s *causeSet) addComponent(key rootCauseKey, component analysis.Component) error {
	if err := validateComponent(component); err != nil {
		return err
	}
	cause, ok := s.byKey[key]
	if !ok {
		return errors.New("component: root cause was not asserted")
	}
	for i := range cause.Components {
		existing := &cause.Components[i]
		if existing.Entity != component.Entity {
			continue
		}
		same, err := sameAction(existing.Action, component.Action)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
		return fmt.Errorf("component %q: conflicting actions", component.Entity)
	}
	cause.Components = append(cause.Components, component)
	return nil
}

func sameAction(a, b *analysis.RecommendedAction) (bool, error) {
	if a == nil || b == nil {
		return a == nil && b == nil, nil
	}
	av, err := json.Marshal(a.Value)
	if err != nil {
		return false, err
	}
	bv, err := json.Marshal(b.Value)
	if err != nil {
		return false, err
	}
	return a.Code == b.Code && a.MOInstance == b.MOInstance && a.Op == b.Op && string(av) == string(bv), nil
}

func (s *causeSet) mergeFrom(other *causeSet) error {
	for _, key := range other.order {
		otherCause := other.byKey[key]
		s.assert(*otherCause)
		for _, component := range otherCause.Components {
			if err := s.addComponent(key, component); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *causeSet) finalize() []analysis.RootCause {
	out := make([]analysis.RootCause, 0, len(s.order))
	for _, key := range s.order {
		cause := *s.byKey[key]
		cause.Components = cloneComponents(cause.Components)
		sort.SliceStable(cause.Components, func(i, j int) bool {
			return cause.Components[i].Entity < cause.Components[j].Entity
		})
		out = append(out, cause)
	}
	return out
}
