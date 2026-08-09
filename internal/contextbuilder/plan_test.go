package contextbuilder

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"re/internal/analysis"
)

func thresholdAlert() analysis.Alert {
	return analysis.Alert{ID: "a1", ProbableCause: "THRESHOLD_CROSSING"}
}

func linkAlert() analysis.Alert {
	return analysis.Alert{ID: "a2", ProbableCause: "LINK_TO_PEER_SIPGW_DOWN"}
}

func TestResolveMergesEveryMatch(t *testing.T) {
	// Declared out of name order, with overlapping targets, so the result
	// proves both the union and the sort rather than echoing the input.
	profiles := []ContextProfile{
		{
			Name:     "second",
			Selector: Selector{ProbableCauses: []string{"LINK_TO_PEER_SIPGW_DOWN"}},
			Providers: ProviderSpec{
				VDU: []string{"ims.vdu_c", "ims.vdu_a"},
				Link: []LinkTarget{
					{SrcPath: "ims.vdu_c.vnfc_c_1", DstPath: "ims.vdu_a.vnfc_a_1"},
					{SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1"},
				},
			},
		},
		{
			Name:     "first",
			Selector: Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
			Providers: ProviderSpec{
				VDU:  []string{"ims.vdu_b", "ims.vdu_a"},
				Link: []LinkTarget{{SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1"}},
				Configuration: []ConfigurationTarget{
					{Path: "ims.vdu_b.vnfc_b_1", Key: "k2", URL: "http://api/k2"},
					{Path: "ims.vdu_a.vnfc_a_1", Key: "k1", URL: "http://api/k1"},
				},
			},
		},
		{
			Name:      "unmatched",
			Selector:  Selector{ProbableCauses: []string{"SOMETHING_ELSE"}},
			Providers: ProviderSpec{VDU: []string{"ims.vdu_never"}},
		},
	}

	plan, err := resolve([]analysis.Alert{thresholdAlert(), linkAlert()}, profiles)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}

	if want := []string{"first", "second"}; !reflect.DeepEqual(plan.Profiles, want) {
		t.Errorf("profiles = %v, want %v", plan.Profiles, want)
	}
	if want := []string{"ims.vdu_a", "ims.vdu_b", "ims.vdu_c"}; !reflect.DeepEqual(plan.VDUs, want) {
		t.Errorf("vdus = %v, want %v", plan.VDUs, want)
	}
	wantLinks := []LinkTarget{
		{SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1"},
		{SrcPath: "ims.vdu_c.vnfc_c_1", DstPath: "ims.vdu_a.vnfc_a_1"},
	}
	if !reflect.DeepEqual(plan.Links, wantLinks) {
		t.Errorf("links = %v, want %v", plan.Links, wantLinks)
	}
	wantConfig := []ConfigurationTarget{
		{Path: "ims.vdu_a.vnfc_a_1", Key: "k1", URL: "http://api/k1"},
		{Path: "ims.vdu_b.vnfc_b_1", Key: "k2", URL: "http://api/k2"},
	}
	if !reflect.DeepEqual(plan.Configuration, wantConfig) {
		t.Errorf("configuration = %v, want %v", plan.Configuration, wantConfig)
	}
	if !plan.HasWork() {
		t.Error("HasWork() = false")
	}
}

// Merging is a set union, so the profile order cannot change the outcome.
func TestResolveIsOrderIndependent(t *testing.T) {
	a := ContextProfile{
		Name:      "alpha",
		Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
		Providers: ProviderSpec{VDU: []string{"ims.vdu_b"}},
	}
	b := ContextProfile{
		Name:      "beta",
		Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
		Providers: ProviderSpec{VDU: []string{"ims.vdu_a"}},
	}

	forward, err := resolve([]analysis.Alert{thresholdAlert()}, []ContextProfile{a, b})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := resolve([]analysis.Alert{thresholdAlert()}, []ContextProfile{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Errorf("plan depends on profile order:\n%+v\n%+v", forward, reverse)
	}
}

// The same (path, key) from two profiles is a duplicate when the URL agrees
// and a definition error when it does not — the engine has no basis to prefer
// one operator's URL over another's.
func TestResolveConfigurationURLConflict(t *testing.T) {
	shared := ConfigurationTarget{Path: "ims.vdu_a.vnfc_a_1", Key: "k", URL: "http://api/k"}
	profiles := []ContextProfile{
		{
			Name:      "alpha",
			Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
			Providers: ProviderSpec{Configuration: []ConfigurationTarget{shared}},
		},
		{
			Name:      "beta",
			Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
			Providers: ProviderSpec{Configuration: []ConfigurationTarget{shared}},
		},
	}

	plan, err := resolve([]analysis.Alert{thresholdAlert()}, profiles)
	if err != nil {
		t.Fatalf("identical targets must merge, got %v", err)
	}
	if len(plan.Configuration) != 1 {
		t.Errorf("configuration = %v, want one deduplicated target", plan.Configuration)
	}

	profiles[1].Providers.Configuration = []ConfigurationTarget{{
		Path: shared.Path, Key: shared.Key, URL: "http://other/k",
	}}
	_, err = resolve([]analysis.Alert{thresholdAlert()}, profiles)
	var defErr *DefinitionError
	if !errors.As(err, &defErr) {
		t.Fatalf("error = %v, want *DefinitionError", err)
	}
	// Both sides have to be named or the operator is left grepping every
	// enabled profile for the key.
	for _, want := range []string{"alpha", "beta", "http://api/k", "http://other/k"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveNoMatch(t *testing.T) {
	profiles := []ContextProfile{{
		Name:      "p",
		Selector:  Selector{ProbableCauses: []string{"LINK_DOWN"}},
		Providers: ProviderSpec{VDU: []string{"ims.vdu_a"}},
	}}

	_, err := resolve([]analysis.Alert{thresholdAlert()}, profiles)
	if !errors.Is(err, ErrContextProfileNotFound) {
		t.Fatalf("error = %v, want ErrContextProfileNotFound", err)
	}
	if err.Error() != "missing context_profile" {
		t.Errorf("message = %q, want %q", err, "missing context_profile")
	}
}

func TestResolveNoProfilesAtAll(t *testing.T) {
	if _, err := resolve([]analysis.Alert{thresholdAlert()}, nil); !errors.Is(err, ErrContextProfileNotFound) {
		t.Fatalf("error = %v, want ErrContextProfileNotFound", err)
	}
}

// A profile that never went through DecodeProfile is validated here, so no
// unchecked target can reach a provider.
func TestResolveValidatesMatchedProfiles(t *testing.T) {
	profiles := []ContextProfile{{
		Name:      "p",
		Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
		Providers: ProviderSpec{VDU: []string{"not a path"}},
	}}

	_, err := resolve([]analysis.Alert{thresholdAlert()}, profiles)
	var defErr *DefinitionError
	if !errors.As(err, &defErr) {
		t.Fatalf("error = %v, want *DefinitionError", err)
	}
	if defErr.Field != "providers.vdu[0]" {
		t.Errorf("field = %q", defErr.Field)
	}
}

// An unmatched profile is not validated: it contributes nothing, and failing
// the request over a definition this request never touches would make one bad
// profile break every unrelated incident.
func TestResolveIgnoresUnmatchedInvalidProfiles(t *testing.T) {
	profiles := []ContextProfile{
		{
			Name:      "good",
			Selector:  Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
			Providers: ProviderSpec{VDU: []string{"ims.vdu_a"}},
		},
		{
			Name:      "bad",
			Selector:  Selector{ProbableCauses: []string{"SOMETHING_ELSE"}},
			Providers: ProviderSpec{VDU: []string{"not a path"}},
		},
	}

	plan, err := resolve([]analysis.Alert{thresholdAlert()}, profiles)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if !reflect.DeepEqual(plan.Profiles, []string{"good"}) {
		t.Errorf("profiles = %v", plan.Profiles)
	}
}

func TestPlanHasWork(t *testing.T) {
	profiles := []ContextProfile{{
		Name:     "p",
		Selector: Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
	}}

	plan, err := resolve([]analysis.Alert{thresholdAlert()}, profiles)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if plan.HasWork() {
		t.Error("HasWork() = true for a profile that asked for nothing")
	}
	if !reflect.DeepEqual(plan.Profiles, []string{"p"}) {
		t.Errorf("profiles = %v, a matched profile is still reported", plan.Profiles)
	}
}
