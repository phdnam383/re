package contextbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"re/internal/analysis"
)

func fullProfile() ContextProfile {
	return ContextProfile{
		Name:     "p",
		Selector: Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
		Providers: ProviderSpec{
			VDU: []string{"ims.vdu_b", "ims.vdu_a"},
			Link: []LinkTarget{
				{SrcPath: "ims.vdu_b.vnfc_b_1", DstPath: "ims.vdu_a.vnfc_a_1"},
				{SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1"},
			},
			Configuration: []ConfigurationTarget{
				{Path: "ims.vdu_b.vnfc_b_1", Key: "k2", URL: "http://api/k2"},
				{Path: "ims.vdu_a.vnfc_a_1", Key: "k1", URL: "http://api/k1"},
			},
		},
	}
}

func populatedProviders() (*fakeVDUProvider, *fakeLinkProvider, *fakeConfigProvider) {
	vdu := &fakeVDUProvider{
		vdus: map[string]analysis.VDU{
			"ims.vdu_a": {Path: "ims.vdu_a", Name: "a", Replicas: 1},
			"ims.vdu_b": {Path: "ims.vdu_b", Name: "b", Replicas: 2},
		},
		vnfcs: map[string][]analysis.VNFC{
			"ims.vdu_a": {{Path: "ims.vdu_a.vnfc_a_1", VDUPath: "ims.vdu_a", Name: "a-1", Status: "RUNNING"}},
			"ims.vdu_b": {{Path: "ims.vdu_b.vnfc_b_1", VDUPath: "ims.vdu_b", Name: "b-1", Status: "TERMINATED"}},
		},
	}
	link := &fakeLinkProvider{links: map[LinkTarget]analysis.Link{
		{SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1"}: {
			SrcPath: "ims.vdu_a.vnfc_a_1", DstPath: "ims.vdu_b.vnfc_b_1", Protocol: "SIP", Status: "DOWN",
		},
		{SrcPath: "ims.vdu_b.vnfc_b_1", DstPath: "ims.vdu_a.vnfc_a_1"}: {
			SrcPath: "ims.vdu_b.vnfc_b_1", DstPath: "ims.vdu_a.vnfc_a_1", Protocol: "SIP", Status: "DOWN",
		},
	}}
	config := &fakeConfigProvider{values: map[configKey]any{
		{path: "ims.vdu_a.vnfc_a_1", key: "k1"}: float64(5),
		{path: "ims.vdu_b.vnfc_b_1", key: "k2"}: "on",
	}}
	return vdu, link, config
}

func newTestBuilder(t *testing.T, profiles *fakeProfiles, vdu VDUProvider, link LinkProvider, config ConfigurationProvider) *Builder {
	t.Helper()
	b, err := New(Options{
		Profiles: profiles, VDU: vdu, Link: link, Configuration: config, Clock: fixedClock(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return b
}

func thresholdInput() analysis.ContextInput {
	return analysis.ContextInput{
		RequestID: "req-1",
		Incident:  "inc-1",
		Alerts:    []analysis.Alert{thresholdAlert()},
	}
}

func TestNewRequiresPorts(t *testing.T) {
	vdu, link, config := populatedProviders()
	full := Options{Profiles: &fakeProfiles{}, VDU: vdu, Link: link, Configuration: config}

	tests := map[string]func(*Options){
		"profiles":      func(o *Options) { o.Profiles = nil },
		"vdu":           func(o *Options) { o.VDU = nil },
		"link":          func(o *Options) { o.Link = nil },
		"configuration": func(o *Options) { o.Configuration = nil },
	}
	for name, drop := range tests {
		t.Run(name, func(t *testing.T) {
			opts := full
			drop(&opts)
			if _, err := New(opts); err == nil {
				t.Errorf("New() without the %s port succeeded", name)
			}
		})
	}

	if _, err := New(full); err != nil {
		t.Errorf("New() error = %v", err)
	}
}

func TestBuildSnapshot(t *testing.T) {
	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)

	snap, err := b.Build(context.Background(), thresholdInput())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if snap.Status != analysis.StatusComplete {
		t.Errorf("status = %s, want %s", snap.Status, analysis.StatusComplete)
	}
	if !reflect.DeepEqual(snap.Profiles, []string{"p"}) {
		t.Errorf("profiles = %v", snap.Profiles)
	}
	if snap.Input.RequestID != "req-1" || snap.Input.Incident != "inc-1" {
		t.Errorf("input not retained: %+v", snap.Input)
	}
	if !snap.BuiltAt.Equal(fixedNow) {
		t.Errorf("built_at = %s, want the injected clock's %s", snap.BuiltAt, fixedNow)
	}
	if len(snap.MissingContext) != 0 {
		t.Errorf("missing = %+v", snap.MissingContext)
	}

	// The fakes answer in reverse order, so sorted output is the builder's
	// doing (see fakes_test.go).
	assertOrder(t, "vdus", []string{snap.VDUs[0].Path, snap.VDUs[1].Path},
		[]string{"ims.vdu_a", "ims.vdu_b"})
	assertOrder(t, "vnfcs", []string{snap.VNFCs[0].Path, snap.VNFCs[1].Path},
		[]string{"ims.vdu_a.vnfc_a_1", "ims.vdu_b.vnfc_b_1"})
	assertOrder(t, "links", []string{snap.Links[0].SrcPath, snap.Links[1].SrcPath},
		[]string{"ims.vdu_a.vnfc_a_1", "ims.vdu_b.vnfc_b_1"})
	assertOrder(t, "configuration", []string{snap.Configuration[0].Key, snap.Configuration[1].Key},
		[]string{"k1", "k2"})
}

func assertOrder(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s order = %v, want %v", what, got, want)
	}
}

func TestBuildNoMatchingProfileCallsNoProvider(t *testing.T) {
	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)

	snap, err := b.Build(context.Background(), analysis.ContextInput{
		RequestID: "req-1",
		Alerts:    []analysis.Alert{{ID: "a", ProbableCause: "SOMETHING_ELSE"}},
	})
	if !errors.Is(err, ErrContextProfileNotFound) {
		t.Fatalf("error = %v, want ErrContextProfileNotFound", err)
	}
	if !reflect.DeepEqual(snap, analysis.ContextSnapshot{}) {
		t.Errorf("snapshot = %+v, want the zero value", snap)
	}
	if vdu.calls.Load() != 0 || link.calls.Load() != 0 || config.calls.Load() != 0 {
		t.Error("a provider ran without a matching profile")
	}
}

func TestBuildSkipsProvidersWithNoWork(t *testing.T) {
	profile := fullProfile()
	profile.Providers.Link = nil
	profile.Providers.Configuration = nil

	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{profile}}, vdu, link, config)

	snap, err := b.Build(context.Background(), thresholdInput())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if link.calls.Load() != 0 || config.calls.Load() != 0 {
		t.Error("a provider with no targets was called")
	}
	if vdu.calls.Load() != 1 {
		t.Errorf("vdu calls = %d, want 1", vdu.calls.Load())
	}
	if snap.Status != analysis.StatusComplete {
		t.Errorf("status = %s", snap.Status)
	}
	// Empty rather than absent, so the serialised shape does not depend on
	// which providers a profile happened to name.
	if snap.Links == nil || snap.Configuration == nil {
		t.Error("empty collections must not be nil")
	}
}

// The three providers must overlap in time. A barrier proves it directly: if
// they ran one after another the first would wait for peers that never arrive.
func TestBuildRunsProvidersConcurrently(t *testing.T) {
	gate := newBarrier(3)
	hold := func(context.Context) {
		if err := gate.arrive(5 * time.Second); err != nil {
			t.Error(err)
		}
	}

	vdu, link, config := populatedProviders()
	vdu.before, link.before, config.before = hold, hold, hold

	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)
	if _, err := b.Build(context.Background(), thresholdInput()); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestBuildProviderFailureIsPartial(t *testing.T) {
	vdu, link, config := populatedProviders()
	vdu.err = errors.New("connection refused")

	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)
	snap, err := b.Build(context.Background(), thresholdInput())
	if err != nil {
		t.Fatalf("Build() error = %v, a provider failure is not a build failure", err)
	}
	if snap.Status != analysis.StatusPartial {
		t.Errorf("status = %s, want %s", snap.Status, analysis.StatusPartial)
	}

	// Every target the failed provider was given is named, and nothing it may
	// have returned alongside the error survives.
	want := []analysis.MissingContext{
		{Provider: analysis.ProviderVDU, Entity: "ims.vdu_a", Reason: analysis.ReasonQueryFailed},
		{Provider: analysis.ProviderVDU, Entity: "ims.vdu_b", Reason: analysis.ReasonQueryFailed},
	}
	if !reflect.DeepEqual(snap.MissingContext, want) {
		t.Errorf("missing = %+v, want %+v", snap.MissingContext, want)
	}
	if len(snap.VDUs) != 0 || len(snap.VNFCs) != 0 {
		t.Errorf("failed provider contributed rows: %+v", snap.VDUs)
	}

	// The other providers' results are kept.
	if len(snap.Links) != 2 || len(snap.Configuration) != 2 {
		t.Errorf("other providers were discarded: links=%d config=%d", len(snap.Links), len(snap.Configuration))
	}
}

func TestBuildMissingTargetIsPartial(t *testing.T) {
	vdu, link, config := populatedProviders()
	delete(vdu.vdus, "ims.vdu_b")
	delete(config.values, configKey{path: "ims.vdu_b.vnfc_b_1", key: "k2"})

	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)
	snap, err := b.Build(context.Background(), thresholdInput())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if snap.Status != analysis.StatusPartial {
		t.Errorf("status = %s", snap.Status)
	}

	// Missing entries are ordered by provider first, so they read in the same
	// VDU → LINK → CONFIGURATION order the collections do.
	want := []analysis.MissingContext{
		{Provider: analysis.ProviderVDU, Entity: "ims.vdu_b", Reason: analysis.ReasonNotFound},
		{Provider: analysis.ProviderConfiguration, Entity: "ims.vdu_b.vnfc_b_1", Key: "k2", Reason: analysis.ReasonHTTPStatus},
	}
	if !reflect.DeepEqual(snap.MissingContext, want) {
		t.Errorf("missing = %+v, want %+v", snap.MissingContext, want)
	}
	if len(snap.VDUs) != 1 || snap.VDUs[0].Path != "ims.vdu_a" {
		t.Errorf("resolved rows were dropped: %+v", snap.VDUs)
	}
}

func TestBuildCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	vdu, link, config := populatedProviders()
	profiles := &fakeProfiles{profiles: []ContextProfile{fullProfile()}}
	b := newTestBuilder(t, profiles, vdu, link, config)

	if _, err := b.Build(ctx, thresholdInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if profiles.calls.Load() != 0 {
		t.Error("profiles were loaded for a cancelled request")
	}
}

// A caller that walks away mid-flight gets ctx.Err(), not a PARTIAL snapshot:
// every provider fails once the context is done, and reporting that as a
// degraded context would describe the caller's own decision as an outage.
func TestBuildCancelledDuringProvidersReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	vdu, link, config := populatedProviders()
	vdu.err = errors.New("query cancelled")
	vdu.before = func(context.Context) { cancel() }

	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)
	snap, err := b.Build(ctx, thresholdInput())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if snap.Status == analysis.StatusPartial {
		t.Error("cancellation was reported as a degraded snapshot")
	}
}

func TestBuildPropagatesProfileLoadFailure(t *testing.T) {
	loadErr := errors.New("dial tcp: connection refused")
	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{err: loadErr}, vdu, link, config)

	if _, err := b.Build(context.Background(), thresholdInput()); !errors.Is(err, loadErr) {
		t.Fatalf("error = %v, want %v", err, loadErr)
	}
	if vdu.calls.Load() != 0 {
		t.Error("a provider ran after the profiles failed to load")
	}
}

func TestBuildPropagatesDefinitionError(t *testing.T) {
	profile := fullProfile()
	profile.Providers.VDU = []string{"not a path"}

	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{profile}}, vdu, link, config)

	var defErr *DefinitionError
	if _, err := b.Build(context.Background(), thresholdInput()); !errors.As(err, &defErr) {
		t.Fatalf("error = %v, want *DefinitionError", err)
	}
	if vdu.calls.Load() != 0 {
		t.Error("a provider ran with an unvalidated target")
	}
}

// Repeated builds over the same data must serialise identically. The fan-out
// completes in a different order run to run, so anything order-dependent that
// survived assembly would show up here rather than in production.
func TestBuildIsDeterministic(t *testing.T) {
	var first []byte
	for i := 0; i < 20; i++ {
		vdu, link, config := populatedProviders()
		delete(vdu.vdus, "ims.vdu_b")
		b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{fullProfile()}}, vdu, link, config)

		snap, err := b.Build(context.Background(), thresholdInput())
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		encoded, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = encoded
			continue
		}
		if string(encoded) != string(first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, encoded)
		}
	}
}

func TestBuildEmptyPlanIsComplete(t *testing.T) {
	profile := ContextProfile{
		Name:     "empty",
		Selector: Selector{ProbableCauses: []string{"THRESHOLD_CROSSING"}},
	}
	vdu, link, config := populatedProviders()
	b := newTestBuilder(t, &fakeProfiles{profiles: []ContextProfile{profile}}, vdu, link, config)

	snap, err := b.Build(context.Background(), thresholdInput())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if snap.Status != analysis.StatusComplete {
		t.Errorf("status = %s, nothing was requested so nothing is missing", snap.Status)
	}
	if vdu.calls.Load()+link.calls.Load()+config.calls.Load() != 0 {
		t.Error("a provider ran for an empty plan")
	}
}
