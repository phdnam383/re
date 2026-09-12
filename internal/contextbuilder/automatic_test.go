package contextbuilder

import (
	"context"
	"reflect"
	"testing"

	"re/internal/analysis"
)

func TestApplyAutomaticPlanAddsRemoteIPLink(t *testing.T) {
	in := analysis.ContextInput{Alerts: []analysis.Alert{{
		AdditionalInformation: map[string]any{analysis.AdditionalInformationRemoteIP: " 10.55.70.37 "},
	}}}
	plan := Plan{Links: []LinkTarget{{Target: "profile.peer"}}}

	got := applyAutomaticPlan(in, plan)
	want := []LinkTarget{{Target: "10.55.70.37"}, {Target: "profile.peer"}}
	if !reflect.DeepEqual(got.Links, want) {
		t.Fatalf("links = %+v, want %+v", got.Links, want)
	}
}

func TestApplyAutomaticPlanDeduplicatesProfileTarget(t *testing.T) {
	in := analysis.ContextInput{Alerts: []analysis.Alert{{
		AdditionalInformation: map[string]any{analysis.AdditionalInformationRemoteIP: "10.55.70.37"},
	}}}
	plan := Plan{Links: []LinkTarget{{Target: "10.55.70.37"}}}

	got := applyAutomaticPlan(in, plan)
	if len(got.Links) != 1 || got.Links[0].Target != "10.55.70.37" {
		t.Fatalf("links = %+v, want one deduplicated target", got.Links)
	}
}

func TestApplyAutomaticPlanIgnoresInvalidRemoteIP(t *testing.T) {
	for _, in := range []analysis.ContextInput{
		{},
		{Alerts: []analysis.Alert{{}}},
		{Alerts: []analysis.Alert{{AdditionalInformation: map[string]any{
			analysis.AdditionalInformationRemoteIP: "   ",
		}}}},
		{Alerts: []analysis.Alert{{AdditionalInformation: map[string]any{
			analysis.AdditionalInformationRemoteIP: float64(10),
		}}}},
	} {
		got := applyAutomaticPlan(in, Plan{})
		if len(got.Links) != 0 {
			t.Fatalf("links = %+v, want no automatic target", got.Links)
		}
	}
}

type automaticProfiles []ContextProfile

func (p automaticProfiles) LoadEnabled(context.Context) ([]ContextProfile, error) { return p, nil }

type unusedVDUProvider struct{}

func (unusedVDUProvider) FetchVDUs(context.Context, []string) (VDUResult, error) {
	return VDUResult{}, nil
}

type unusedConfigurationProvider struct{}

func (unusedConfigurationProvider) FetchConfiguration(context.Context, string, []string) (ConfigurationResult, error) {
	return ConfigurationResult{}, nil
}

type unusedMetricProvider struct{}

func (unusedMetricProvider) FetchMetrics(context.Context, string, []string) (MetricResult, error) {
	return MetricResult{}, nil
}

type recordingLinkProvider struct {
	vnfcPath string
	targets  []LinkTarget
}

func (p *recordingLinkProvider) FetchLinks(_ context.Context, vnfcPath string, targets []LinkTarget) (LinkResult, error) {
	p.vnfcPath = vnfcPath
	p.targets = append([]LinkTarget(nil), targets...)
	entries := make([]analysis.LinkEntry, 0, len(targets))
	for _, target := range targets {
		entries = append(entries, analysis.LinkEntry{Target: target.Target, Status: "OK"})
	}
	return LinkResult{Entries: entries}, nil
}

func TestBuilderFetchesAutomaticRemoteIPLink(t *testing.T) {
	link := &recordingLinkProvider{}
	builder, err := New(Options{
		Profiles: automaticProfiles{{
			Name:     "link-profile-without-explicit-target",
			Selector: Selector{ProbableCauses: []string{"LINK_DOWN"}},
		}},
		VDU:           unusedVDUProvider{},
		Configuration: unusedConfigurationProvider{},
		Link:          link,
		Metric:        unusedMetricProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const sourcePath = "ims.vdu_sb_logic.vnfc_1"
	snapshot, err := builder.Build(context.Background(), analysis.ContextInput{
		RequestID: "automatic-link",
		Alerts: []analysis.Alert{{
			SourcePath:    sourcePath,
			ProbableCause: "LINK_DOWN",
			AdditionalInformation: map[string]any{
				analysis.AdditionalInformationRemoteIP: "10.55.70.37",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if link.vnfcPath != sourcePath {
		t.Errorf("vnfcPath = %q, want %q", link.vnfcPath, sourcePath)
	}
	wantTargets := []LinkTarget{{Target: "10.55.70.37"}}
	if !reflect.DeepEqual(link.targets, wantTargets) {
		t.Errorf("targets = %+v, want %+v", link.targets, wantTargets)
	}
	if len(snapshot.Links) != 1 || snapshot.Links[0].Target != "10.55.70.37" {
		t.Fatalf("snapshot links = %+v, want automatic remote link", snapshot.Links)
	}
	if snapshot.Status != analysis.StatusComplete {
		t.Fatalf("snapshot status = %q, want %q", snapshot.Status, analysis.StatusComplete)
	}
}
