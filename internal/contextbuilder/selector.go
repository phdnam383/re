package contextbuilder

import (
	"strings"

	"re/internal/analysis"
)

// Matches reports whether any single alert satisfies the selector.
//
// The quantifier is "there exists one alert", and the conjunction is inside
// that alert. A request whose alerts collectively mention a probable cause and
// an alert type does not match a selector naming both — the profile is a
// statement about a kind of alert, not about a bag of them.
func (s Selector) Matches(alerts []analysis.Alert) bool {
	for _, a := range alerts {
		if s.MatchesAlert(a) {
			return true
		}
	}
	return false
}

// MatchesAlert reports whether one alert satisfies every populated clause.
//
// An unpopulated clause is not a constraint, so it is skipped rather than
// treated as "must be empty". Validate already rejects a selector where every
// clause is unpopulated, so this can never degenerate into matching everything.
func (s Selector) MatchesAlert(a analysis.Alert) bool {
	if len(s.ProbableCauses) > 0 && !containsFold(s.ProbableCauses, a.ProbableCause) {
		return false
	}
	if len(s.AlertTypes) > 0 && !containsFold(s.AlertTypes, a.AlertType) {
		return false
	}

	for key, want := range s.AdditionalInformation {
		// JSON object keys are case-sensitive, so the lookup is exact even
		// though the values below are not. "metric" and "Metric" are two
		// different fields of the payload; MAJOR and major are one severity.
		got, present := a.AdditionalInformation[key]
		if !present {
			return false
		}
		// An empty list asserts presence only — the key is the whole clause.
		if len(want) == 0 {
			continue
		}
		if !matchesAny(want, got) {
			return false
		}
	}
	return true
}

func matchesAny(want []any, got any) bool {
	for _, w := range want {
		if matchesValue(w, got) {
			return true
		}
	}
	return false
}

// matchesValue compares two decoded JSON values by type.
//
// There is no cross-type coercion: the alert payload carries typed values
// (observed_value: 93.5, threshold_value: 85) and a selector asking for the
// string "85" is asking for something the alert does not say. Silently
// coercing would make a profile match payloads its author never described.
//
// Strings are the one case-insensitive comparison, matching the probable-cause
// and alert-type vocabulary above. Objects and arrays never match and cannot
// reach here — Validate rejects them in a selector.
func matchesValue(want, got any) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		return ok && strings.EqualFold(w, g)
	case float64:
		g, ok := got.(float64)
		return ok && w == g
	case bool:
		g, ok := got.(bool)
		return ok && w == g
	case nil:
		return got == nil
	default:
		return false
	}
}

func containsFold(want []string, got string) bool {
	for _, w := range want {
		if strings.EqualFold(w, got) {
			return true
		}
	}
	return false
}
