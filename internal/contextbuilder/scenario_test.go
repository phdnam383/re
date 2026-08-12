// Scenario tests drive the three shipped context profiles end to end: the
// alerts a scenario raises, through selector matching and plan merging, to the
// snapshot the rule engine will read.
//
// They live in the external test package because they wire the real
// Configuration Provider, which imports contextbuilder.
//
// The topology fixtures mirror db/seed_test.sql. They are held in Go rather
// than read from PostgreSQL so the scenarios run everywhere and so the
// timestamps are fixed — the seed uses now() for several links, which no
// golden file could pin.
package contextbuilder_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/contextbuilder/configuration"
)

var (
	// seedTime is db/seed_test.sql's fixed row timestamp.
	seedTime = time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	// diagwLinkTime is the DIAGW link's own seeded timestamp.
	diagwLinkTime = time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC)
	// The second instance of each core VDU carries its own seeded timestamp,
	// one second after the first.
	sipgwLink2Time = time.Date(2026, 6, 18, 0, 0, 1, 0, time.UTC)
	diagwLink2Time = time.Date(2026, 6, 18, 1, 0, 1, 0, time.UTC)
	// buildTime is what the injected clock reports.
	buildTime = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
)

// --- fixtures ------------------------------------------------------------

func vduRow(path, name, typ, namespace, workload string, replicas int, selector string, nfConfig map[string]any) analysis.VDU {
	return analysis.VDU{
		Path: path, Name: name, Type: typ, Namespace: namespace, Workload: workload,
		Replicas: replicas, Selector: selector, NFConfig: nfConfig, UpdatedAt: seedTime,
	}
}

func vnfcRow(path, vduPath, name, status, uid string, instanceConfig map[string]any) analysis.VNFC {
	return analysis.VNFC{
		Path: path, VDUPath: vduPath, Name: name, Status: status, K8sUID: uid,
		InstanceConfig: instanceConfig, CreatedAt: seedTime, UpdatedAt: seedTime,
	}
}

func linkRow(src, dst, protocol, status string, at time.Time) analysis.Link {
	return analysis.Link{
		SrcPath: src, DstPath: dst, Protocol: protocol, Status: status,
		CreatedAt: at, UpdatedAt: at,
	}
}

type topology struct {
	vdus  map[string]analysis.VDU
	vnfcs map[string][]analysis.VNFC

	// links is a slice, not a map keyed by target: a target now names subtree
	// roots, so which rows it selects is decided by matching rather than by
	// lookup — exactly as the SQL does it.
	links []analysis.Link
}

func seedTopology() topology {
	t := topology{
		vdus:  map[string]analysis.VDU{},
		vnfcs: map[string][]analysis.VNFC{},
	}

	add := func(vdu analysis.VDU, vnfcs ...analysis.VNFC) {
		t.vdus[vdu.Path] = vdu
		t.vnfcs[vdu.Path] = vnfcs
	}

	// SIPGW
	add(vduRow("ims.vdu_sb_sip_core", "sb_sip_core", "SIPGW", "dev-sb", "StatefulSet", 2, "app=sb-sip-core",
		map[string]any{"sip_port": float64(5060), "transport": "TCP"}),
		vnfcRow("ims.vdu_sb_sip_core.vnfc_sb_sip_core_1", "ims.vdu_sb_sip_core", "vnfc_sb_sip_core_1", "RUNNING", "uid-sb-sip-core-1",
			map[string]any{"ip": "10.55.60.10", "zone": "zone-a"}),
		vnfcRow("ims.vdu_sb_sip_core.vnfc_sb_sip_core_2", "ims.vdu_sb_sip_core", "vnfc_sb_sip_core_2", "RUNNING", "uid-sb-sip-core-2",
			map[string]any{"ip": "10.55.60.11", "zone": "zone-b"}))

	add(vduRow("ims.vdu_cs_loadbalancer_icscf", "cs_loadbalancer_icscf", "SIPGW", "dev-sb", "Deployment", 1, "app=cs-lb-icscf",
		map[string]any{"listen_port": float64(5060), "protocol": "SIP"}),
		vnfcRow("ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1", "ims.vdu_cs_loadbalancer_icscf", "vnfc_cs_loadbalancer_icscf_1", "TERMINATED", "",
			map[string]any{"ip": "10.55.70.37", "failure_reason": "SIP health check failed"}))

	add(vduRow("ims.vdu_cs_sip_icscf", "cs_sip_icscf", "SIPGW", "dev-sb", "StatefulSet", 1, "app=cs-sip-icscf",
		map[string]any{"sip_port": float64(5060), "role": "I-CSCF"}),
		vnfcRow("ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1", "ims.vdu_cs_sip_icscf", "vnfc_cs_sip_icscf_1", "TERMINATED", "",
			map[string]any{"ip": "10.55.70.38", "failure_reason": "Pod not ready"}))

	add(vduRow("ims.vdu_cs_logic", "cs_logic", "LOGIC", "dev-sb", "StatefulSet", 1, "app=cs-logic",
		map[string]any{"role": "CALL_CONTROL"}),
		vnfcRow("ims.vdu_cs_logic.vnfc_cs_logic_1", "ims.vdu_cs_logic", "vnfc_cs_logic_1", "TERMINATED", "",
			map[string]any{"ip": "10.55.70.39", "failure_reason": "Process exited"}))

	// DIAGW
	add(vduRow("ims.vdu_sb_diameter_core", "sb_diameter_core", "DIAGW", "dev-sb", "StatefulSet", 2, "app=sb-diameter-core",
		map[string]any{"application_protocol": "DIAMETER", "transport": "SCTP"}),
		vnfcRow("ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1", "ims.vdu_sb_diameter_core", "vnfc_sb_diameter_core_1", "RUNNING", "uid-sb-diameter-core-1",
			map[string]any{"ip": "10.56.60.10", "port": float64(3868), "zone": "zone-a"}),
		vnfcRow("ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2", "ims.vdu_sb_diameter_core", "vnfc_sb_diameter_core_2", "RUNNING", "uid-sb-diameter-core-2",
			map[string]any{"ip": "10.56.60.11", "port": float64(3868), "zone": "zone-b"}))

	add(vduRow("ims.vdu_cs_loadbalancer_diagw", "cs_loadbalancer_diagw", "DIAGW", "dev-sb", "Deployment", 1, "app=cs-lb-diagw",
		map[string]any{"application_protocol": "DIAMETER", "transport": "SCTP"}),
		vnfcRow("ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1", "ims.vdu_cs_loadbalancer_diagw", "vnfc_cs_loadbalancer_diagw_1", "TERMINATED", "",
			map[string]any{"ip": "10.56.70.37", "port": float64(3868), "failure_reason": "SCTP association health check failed"}))

	add(vduRow("ims.vdu_cs_diameter_router", "cs_diameter_router", "DIAGW", "dev-sb", "StatefulSet", 1, "app=cs-diameter-router",
		map[string]any{"application_protocol": "DIAMETER", "transport": "SCTP"}),
		vnfcRow("ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1", "ims.vdu_cs_diameter_router", "vnfc_cs_diameter_router_1", "TERMINATED", "",
			map[string]any{"ip": "10.56.70.38", "port": float64(3868), "failure_reason": "Diameter routing process is not ready"}))

	add(vduRow("ims.vdu_cs_diag_logic", "cs_diag_logic", "LOGIC", "dev-sb", "StatefulSet", 1, "app=cs-diag-logic",
		map[string]any{"application_protocol": "HTTP2", "transport": "TCP"}),
		vnfcRow("ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1", "ims.vdu_cs_diag_logic", "vnfc_cs_diag_logic_1", "TERMINATED", "",
			map[string]any{"ip": "10.56.70.39", "port": float64(8080), "failure_reason": "Routing logic process exited"}))

	add(vduRow("ims.vdu_cs_hss_connector", "cs_hss_connector", "DIAGW", "dev-sb", "Deployment", 1, "app=cs-hss-connector",
		map[string]any{"application_protocol": "DIAMETER", "transport": "SCTP"}),
		vnfcRow("ims.vdu_cs_hss_connector.vnfc_cs_hss_connector_1", "ims.vdu_cs_hss_connector", "vnfc_cs_hss_connector_1", "RUNNING", "uid-cs-hss-connector-1",
			map[string]any{"ip": "10.56.70.40", "port": float64(3868)}))

	// TPS — nf_config and instance_config are NULL in the seed.
	add(vduRow("ims.vdu_sb_logic", "sb_logic", "LOGIC", "ims", "StatefulSet", 3, "app=sb-logic", nil),
		vnfcRow("ims.vdu_sb_logic.vnfc_sb_logic_1", "ims.vdu_sb_logic", "sb-logic-1", "RUNNING", "a1b2c3d4-0001-0000-0000-000000000001", nil),
		vnfcRow("ims.vdu_sb_logic.vnfc_sb_logic_2", "ims.vdu_sb_logic", "sb-logic-2", "TERMINATED", "", nil),
		vnfcRow("ims.vdu_sb_logic.vnfc_sb_logic_3", "ims.vdu_sb_logic", "sb-logic-3", "TERMINATED", "", nil))

	// Both instances of each core VDU have an edge to the peer load balancer.
	// The second one is what a VDU-pair target picks up and an instance-pair
	// target never did — and it is in db/seed_test.sql with a fixed timestamp,
	// so a golden can pin it.
	for _, l := range []analysis.Link{
		linkRow("ims.vdu_sb_sip_core.vnfc_sb_sip_core_1", "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1", "SIP", "DOWN", seedTime),
		linkRow("ims.vdu_sb_sip_core.vnfc_sb_sip_core_2", "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1", "SIP", "DOWN", sipgwLink2Time),
		linkRow("ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1", "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1", "DIAMETER", "DOWN", diagwLinkTime),
		linkRow("ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2", "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1", "DIAMETER", "DOWN", diagwLink2Time),
	} {
		t.links = append(t.links, l)
	}

	return t
}

// --- provider stand-ins --------------------------------------------------

type scenarioProfiles struct {
	profiles []contextbuilder.ContextProfile
}

func (s scenarioProfiles) LoadEnabled(context.Context) ([]contextbuilder.ContextProfile, error) {
	return s.profiles, nil
}

type scenarioVDU struct{ topology }

func (s scenarioVDU) FetchVDUs(_ context.Context, paths []string) (contextbuilder.VDUResult, error) {
	var res contextbuilder.VDUResult
	for _, path := range paths {
		vdu, ok := s.vdus[path]
		if !ok {
			res.Missing = append(res.Missing, analysis.MissingContext{
				Provider: analysis.ProviderVDU, Entity: path, Reason: analysis.ReasonNotFound,
			})
			continue
		}
		res.VDUs = append(res.VDUs, vdu)
		res.VNFCs = append(res.VNFCs, s.vnfcs[path]...)
	}
	return res, nil
}

type scenarioLink struct{ topology }

func (s scenarioLink) FetchLinks(_ context.Context, targets []contextbuilder.LinkTarget) (contextbuilder.LinkResult, error) {
	var res contextbuilder.LinkResult
	for _, target := range targets {
		found := false
		for _, link := range s.links {
			if !target.Matches(link.SrcPath, link.DstPath) {
				continue
			}
			found = true
			res.Links = append(res.Links, link)
		}
		if !found {
			res.Missing = append(res.Missing, analysis.MissingContext{
				Provider: analysis.ProviderLink, Entity: target.Entity(), Reason: analysis.ReasonNotFound,
			})
		}
	}
	return res, nil
}

// fixtureTransport answers the exact URLs the profiles declare, so the stored
// production URL — not a rewritten httptest address — is what ends up in the
// golden file. The real Configuration Provider runs on top of it.
type fixtureTransport map[string]string

func (f fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	payload, ok := f[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"no such key"}`)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    req,
	}, nil
}

// --- scenarios -----------------------------------------------------------

func loadProfile(t *testing.T, file string) contextbuilder.ContextProfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "context_profile", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var doc struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Selector    json.RawMessage `json:"selector"`
		Providers   json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", file, err)
	}
	profile, err := contextbuilder.DecodeProfile(doc.Name, doc.Description, doc.Selector, doc.Providers)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return profile
}

// allProfiles is what an engine actually has loaded: every enabled profile,
// not just the scenario's own. A scenario that matched a second profile would
// change the snapshot, so running them all is part of the assertion.
func allProfiles(t *testing.T) []contextbuilder.ContextProfile {
	t.Helper()
	return []contextbuilder.ContextProfile{
		loadProfile(t, "link_to_sipgw_down.json"),
		loadProfile(t, "link_to_diagw_down.json"),
		loadProfile(t, "tps_overloaded.json"),
	}
}

func scenarioBuilder(t *testing.T, config fixtureTransport) *contextbuilder.Builder {
	t.Helper()
	topo := seedTopology()
	clock := contextbuilder.ClockFunc(func() time.Time { return buildTime })

	b, err := contextbuilder.New(contextbuilder.Options{
		Profiles: scenarioProfiles{allProfiles(t)},
		VDU:      scenarioVDU{topo},
		Link:     scenarioLink{topo},
		Configuration: configuration.New(configuration.Options{
			Client: &http.Client{Transport: config},
			Clock:  clock,
		}),
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return b
}

func TestScenarioSIPGWLinkDown(t *testing.T) {
	input := analysis.ContextInput{
		RequestID: "req-sipgw-0001",
		Incident:  "inc-sipgw-0001",
		Alerts: []analysis.Alert{
			{
				ID: "aaaaaaaa-1111-4111-8111-111111111111", SourcePath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
				AlertType: "COMMUNICATIONS_ALERT", ProbableCause: "LINK_TO_PEER_SIPGW_DOWN",
				PerceivedSeverity: "CRITICAL", State: "ACTIVE", CreatedAt: "2026-06-18T00:00:00Z",
				AdditionalInformation: map[string]any{
					"dst_path":    "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
					"remote_ip":   "10.55.70.37",
					"remote_port": float64(5060),
					"transport":   "TCP",
				},
			},
			{
				ID: "aaaaaaaa-2222-4222-8222-222222222222", SourcePath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_2",
				AlertType: "COMMUNICATIONS_ALERT", ProbableCause: "LINK_TO_PEER_SIPGW_DOWN",
				PerceivedSeverity: "CRITICAL", State: "ACTIVE", CreatedAt: "2026-06-18T00:00:01Z",
				AdditionalInformation: map[string]any{
					"dst_path":    "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
					"remote_ip":   "10.55.70.37",
					"remote_port": float64(5060),
					"transport":   "TCP",
				},
			},
		},
	}

	snap := buildScenario(t, scenarioBuilder(t, nil), input)

	assertProfiles(t, snap, "link_to_peer_sipgw_down_0001")
	assertComplete(t, snap)
	// Two links, not one: the profile names a VDU pair, so both sb_sip_core
	// instances' edges to the load balancer come back. An instance-pair target
	// fetched only the first, and would have kept fetching only the first
	// however far the VDU scaled.
	if len(snap.VDUs) != 4 || len(snap.VNFCs) != 5 || len(snap.Links) != 2 {
		t.Errorf("collections = %d vdus, %d vnfcs, %d links", len(snap.VDUs), len(snap.VNFCs), len(snap.Links))
	}
	if len(snap.Configuration) != 0 {
		t.Errorf("configuration = %+v, the profile asked for none", snap.Configuration)
	}
	assertGolden(t, "sipgw_link_down.json", snap)
}

func TestScenarioDIAGWLinkDown(t *testing.T) {
	input := analysis.ContextInput{
		RequestID: "req-diagw-0001",
		Incident:  "inc-diagw-0001",
		Alerts: []analysis.Alert{
			{
				ID: "eeeeeeee-1111-4111-8111-111111111111", SourcePath: "ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1",
				AlertType: "COMMUNICATIONS_ALERT", ProbableCause: "LINK_TO_PEER_DIAGW_DOWN",
				PerceivedSeverity: "CRITICAL", State: "ACTIVE", CreatedAt: "2026-06-18T01:00:00Z",
				AdditionalInformation: map[string]any{
					"dst_path":             "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1",
					"application_protocol": "DIAMETER",
					"transport":            "SCTP",
				},
			},
			{
				ID: "eeeeeeee-2222-4222-8222-222222222222", SourcePath: "ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2",
				AlertType: "COMMUNICATIONS_ALERT", ProbableCause: "LINK_TO_PEER_DIAGW_DOWN",
				PerceivedSeverity: "CRITICAL", State: "ACTIVE", CreatedAt: "2026-06-18T01:00:01Z",
				AdditionalInformation: map[string]any{
					"dst_path":             "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1",
					"application_protocol": "DIAMETER",
					"transport":            "SCTP",
				},
			},
		},
	}

	snap := buildScenario(t, scenarioBuilder(t, nil), input)

	assertProfiles(t, snap, "link_to_peer_diagw_down_0001")
	assertComplete(t, snap)
	if len(snap.VDUs) != 5 || len(snap.VNFCs) != 6 || len(snap.Links) != 2 {
		t.Errorf("collections = %d vdus, %d vnfcs, %d links", len(snap.VDUs), len(snap.VNFCs), len(snap.Links))
	}
	assertGolden(t, "diagw_link_down.json", snap)
}

func TestScenarioTPSOverloaded(t *testing.T) {
	const configURL = "http://api/v1/ims.vdu_sb_logic.vnfc_sb_logic_1/num_of_log_file"

	input := analysis.ContextInput{
		RequestID: "req-tps-0001",
		Incident:  "inc-tps-0001",
		Alerts:    tpsAlerts(),
	}

	// db/seed_test.sql pins the fixture: the API answers 5 for this key.
	snap := buildScenario(t, scenarioBuilder(t, fixtureTransport{configURL: `5`}), input)

	assertProfiles(t, snap, "tps_overloaded_0001")
	assertComplete(t, snap)
	if len(snap.VDUs) != 1 || len(snap.VNFCs) != 3 {
		t.Errorf("collections = %d vdus, %d vnfcs", len(snap.VDUs), len(snap.VNFCs))
	}
	if len(snap.Links) != 0 {
		t.Errorf("links = %+v, the profile asked for none", snap.Links)
	}
	if len(snap.Configuration) != 1 {
		t.Fatalf("configuration = %+v", snap.Configuration)
	}
	entry := snap.Configuration[0]
	if entry.Value != float64(5) || entry.URL != configURL {
		t.Errorf("configuration entry = %+v", entry)
	}
	assertGolden(t, "tps_overloaded.json", snap)
}

// The Configuration Provider must never substitute a declared value for an
// effective one, so an unreachable API degrades the snapshot instead.
func TestScenarioTPSConfigurationUnavailable(t *testing.T) {
	snap := buildScenario(t, scenarioBuilder(t, fixtureTransport{}), analysis.ContextInput{
		RequestID: "req-tps-0002", Incident: "inc-tps-0002", Alerts: tpsAlerts(),
	})

	if snap.Status != analysis.StatusPartial {
		t.Errorf("status = %s, want %s", snap.Status, analysis.StatusPartial)
	}
	if len(snap.Configuration) != 0 {
		t.Errorf("configuration = %+v, nothing was read", snap.Configuration)
	}
	// The topology the same profile asked for is untouched.
	if len(snap.VDUs) != 1 || len(snap.VNFCs) != 3 {
		t.Errorf("topology was discarded with the configuration: %+v", snap.VDUs)
	}
	want := analysis.MissingContext{
		Provider: analysis.ProviderConfiguration,
		Entity:   "ims.vdu_sb_logic.vnfc_sb_logic_1",
		Key:      "number_of_log_file",
		Reason:   analysis.ReasonHTTPStatus,
	}
	if len(snap.MissingContext) != 1 || snap.MissingContext[0] != want {
		t.Errorf("missing = %+v, want %+v", snap.MissingContext, want)
	}
}

func tpsAlerts() []analysis.Alert {
	alert := func(id, createdAt string, observed float64) analysis.Alert {
		return analysis.Alert{
			ID: id, SourcePath: "ims.vdu_sb_logic.vnfc_sb_logic_1",
			AlertType: "QUALITY_OF_SERVICE_ALERT", ProbableCause: "THRESHOLD_CROSSING",
			PerceivedSeverity: "MAJOR", State: "ACTIVE", CreatedAt: createdAt,
			AdditionalInformation: map[string]any{
				"metric":          "overload_ram",
				"observed_value":  observed,
				"threshold_value": float64(85),
			},
		}
	}
	return []analysis.Alert{
		alert("cccccccc-3333-4333-8333-333333333333", "2026-06-18T10:00:00Z", 93.5),
		alert("cccccccc-3333-4333-8333-333333333334", "2026-06-18T10:00:02Z", 94.1),
		alert("cccccccc-3333-4333-8333-333333333335", "2026-06-18T10:00:04Z", 95.0),
	}
}

// --- helpers -------------------------------------------------------------

func buildScenario(t *testing.T, b *contextbuilder.Builder, in analysis.ContextInput) analysis.ContextSnapshot {
	t.Helper()
	snap, err := b.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !snap.BuiltAt.Equal(buildTime) {
		t.Errorf("built_at = %s, want the injected clock's %s", snap.BuiltAt, buildTime)
	}
	return snap
}

func assertProfiles(t *testing.T, snap analysis.ContextSnapshot, want ...string) {
	t.Helper()
	if len(snap.Profiles) != len(want) {
		t.Fatalf("profiles = %v, want %v", snap.Profiles, want)
	}
	for i, name := range want {
		if snap.Profiles[i] != name {
			t.Errorf("profiles[%d] = %q, want %q", i, snap.Profiles[i], name)
		}
	}
}

func assertComplete(t *testing.T, snap analysis.ContextSnapshot) {
	t.Helper()
	if snap.Status != analysis.StatusComplete {
		t.Errorf("status = %s, want %s (missing: %+v)", snap.Status, analysis.StatusComplete, snap.MissingContext)
	}
}

// assertGolden pins the whole serialised snapshot. Regenerate deliberately
// after a contract change with RE_UPDATE_GOLDEN=1; never hand-edit the files.
func assertGolden(t *testing.T, name string, snap analysis.ContextSnapshot) {
	t.Helper()

	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("..", "..", "testdata", "context_builder", name)
	if os.Getenv("RE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with RE_UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("snapshot does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
