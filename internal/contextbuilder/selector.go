package contextbuilder

import (
	"strings"

	"re/internal/analysis"
)

func (s Selector) Matches(alerts []analysis.Alert) bool {
	for _, a := range alerts {
		if s.MatchesAlert(a) {
			return true
		}
	}
	return false
}

func (s Selector) MatchesAlert(a analysis.Alert) bool {
	if len(s.ProbableCauses) > 0 && !containsFold(s.ProbableCauses, a.ProbableCause) {
		return false
	}
	if len(s.AlertTypes) > 0 && !containsFold(s.AlertTypes, a.AlertType) {
		return false
	}
	if len(s.SourcePaths) > 0 && !matchesSourcePath(s.SourcePaths, a.SourcePath) {
		return false
	}

	for key, want := range s.AdditionalInformation {

		got, present := a.AdditionalInformation[key]
		if !present {
			return false
		}

		if len(want) == 0 {
			continue
		}
		if !matchesAny(want, got) {
			return false
		}
	}
	return true
}

func matchesSourcePath(want []string, got string) bool {
	for _, path := range want {
		if underOrEqualFold(path, got) {
			return true
		}
	}
	return false
}

func matchesAny(want []any, got any) bool {
	for _, w := range want {
		if matchesValue(w, got) {
			return true
		}
	}
	return false
}

func matchesValue(want, got any) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		if !ok {
			return false
		}
		base, _ := strings.CutSuffix(w, ".*")
		return underOrEqualFold(base, g)
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

func underOrEqualFold(base, got string) bool {
	if len(got) < len(base) {
		return false
	}
	if !strings.EqualFold(got[:len(base)], base) {
		return false
	}
	rest := got[len(base):]
	return len(rest) == 0 || strings.HasPrefix(rest, ".")
}

func containsFold(want []string, got string) bool {
	for _, w := range want {
		if strings.EqualFold(w, got) {
			return true
		}
	}
	return false
}
