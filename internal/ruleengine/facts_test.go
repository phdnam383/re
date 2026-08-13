package ruleengine

import (
	"testing"

	"re/internal/analysis"
)

// factsSnapshot is the fixture every fact test reads. It carries one VDU with
// three replicas of which one is RUNNING, links in both directions with
// different statuses, and configuration values covering each JSON type.
func factsSnapshot() analysis.ContextSnapshot {
	return analysis.ContextSnapshot{
		Status: analysis.StatusComplete,
		Input: analysis.ContextInput{Alerts: []analysis.Alert{
			{
				ID:            "a-overload-1",
				SourcePath:    "ims.vdu_sb_logic.vnfc_sb_logic_1",
				ProbableCause: "THRESHOLD_CROSSING",
				AdditionalInformation: map[string]any{
					"metric": "overload_ram",
				},
			},
			{
				ID:            "a-overload-2",
				SourcePath:    "ims.vdu_sb_logic.vnfc_sb_logic_1",
				ProbableCause: "threshold_crossing", // lower case on purpose
				AdditionalInformation: map[string]any{
					"metric": "OVERLOAD_CPU",
				},
			},
			{
				// A threshold crossing that is not an overload.
				ID:            "a-latency",
				SourcePath:    "ims.vdu_sb_logic.vnfc_sb_logic_2",
				ProbableCause: "THRESHOLD_CROSSING",
				AdditionalInformation: map[string]any{
					"metric": "latency_p99",
				},
			},
			{
				// An overload metric that is not a threshold crossing.
				ID:            "a-wrong-cause",
				SourcePath:    "ims.vdu_sb_logic.vnfc_sb_logic_3",
				ProbableCause: "COMMUNICATIONS_SUBSYSTEM_FAILURE",
				AdditionalInformation: map[string]any{
					"metric": "overload_ram",
				},
			},
			{
				// A sibling VDU whose path shares a prefix with the one under
				// test. It must never be counted as a descendant.
				ID:            "a-sibling",
				SourcePath:    "ims.vdu_sb_logic_backup.vnfc_1",
				ProbableCause: "THRESHOLD_CROSSING",
				AdditionalInformation: map[string]any{
					"metric": "overload_ram",
				},
			},
			{
				ID:            "a-link",
				SourcePath:    "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
				ProbableCause: "LINK_TO_PEER_SIPGW_DOWN",
			},
		}},
		VDUs: []analysis.VDU{
			{Path: "ims.vdu_sb_logic", Name: "sb_logic", Replicas: 3},
			{Path: "ims.vdu_scaled_to_zero", Name: "idle", Replicas: 0},
			{Path: "ims.vdu_healthy", Name: "healthy", Replicas: 1},
		},
		VNFCs: []analysis.VNFC{
			{Path: "ims.vdu_sb_logic.vnfc_sb_logic_1", VDUPath: "ims.vdu_sb_logic", Status: "RUNNING"},
			{Path: "ims.vdu_sb_logic.vnfc_sb_logic_2", VDUPath: "ims.vdu_sb_logic", Status: "TERMINATED"},
			{Path: "ims.vdu_sb_logic.vnfc_sb_logic_3", VDUPath: "ims.vdu_sb_logic", Status: "UNKNOWN"},
			{Path: "ims.vdu_healthy.vnfc_healthy_1", VDUPath: "ims.vdu_healthy", Status: "running"},
		},
		Links: []analysis.Link{
			{SrcPath: "ims.a", DstPath: "ims.b", Status: "DOWN"},
			{SrcPath: "ims.b", DstPath: "ims.a", Status: "UP"},
			{SrcPath: "ims.c", DstPath: "ims.d", Status: "degraded"},
			{SrcPath: "ims.e", DstPath: "ims.f", Status: "UNKNOWN"},
		},
		Configuration: []analysis.ConfigurationEntry{
			{Path: "ims.x", Key: "number", Value: float64(5)},
			{Path: "ims.x", Key: "zero", Value: float64(0)},
			{Path: "ims.x", Key: "text", Value: "enabled"},
			{Path: "ims.x", Key: "nothing", Value: nil},
			{Path: "ims.x", Key: "flag", Value: true},
			{Path: "ims.x", Key: "goint", Value: 7},
		},
	}
}

func testFacts(t *testing.T) *Facts {
	t.Helper()
	return NewFacts(factsSnapshot())
}

// --- alerts --------------------------------------------------------------

func TestAlertsHasCause(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name  string
		cause string
		want  bool
	}{
		{"present", "LINK_TO_PEER_SIPGW_DOWN", true},
		{"case insensitive", "link_to_peer_sipgw_down", true},
		{"present on several alerts", "THRESHOLD_CROSSING", true},
		{"absent", "EQUIPMENT_MALFUNCTION", false},
		{"empty is not a wildcard", "", false},
		{"substring does not match", "THRESHOLD", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Alert.HasCause(tc.cause); got != tc.want {
				t.Errorf("HasCause(%q) = %v, want %v", tc.cause, got, tc.want)
			}
		})
	}
}

func TestAlertSourcePath(t *testing.T) {
	if got := testFacts(t).Alert.SourcePath(); got != "ims.vdu_sb_logic.vnfc_sb_logic_1" {
		t.Errorf("SourcePath() = %q", got)
	}
	if got := NewFacts(analysis.ContextSnapshot{}).Alert.SourcePath(); got != "" {
		t.Errorf("empty SourcePath() = %q", got)
	}
}

func TestAlertsOverloadScoping(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name   string
		entity string
		want   int
	}{
		{
			// Both overload alerts sit on this VNFC; one of them spells the
			// cause and the metric in a different case.
			name: "entity itself", entity: "ims.vdu_sb_logic.vnfc_sb_logic_1", want: 2,
		},
		{
			// The VDU sees its descendants' alerts, which is the whole reason
			// a VDU-level rule can reason about pod overload.
			name: "descendants roll up", entity: "ims.vdu_sb_logic", want: 2,
		},
		{
			// ims.vdu_sb_logic_backup starts with the same characters but is a
			// different LTREE node.
			name: "prefix is not containment", entity: "ims.vdu_sb_logic_backup", want: 1,
		},
		{name: "threshold crossing on a non-overload metric", entity: "ims.vdu_sb_logic.vnfc_sb_logic_2", want: 0},
		{name: "overload metric under the wrong cause", entity: "ims.vdu_sb_logic.vnfc_sb_logic_3", want: 0},
		{name: "unknown entity", entity: "ims.nothing", want: 0},
		{name: "empty entity matches nothing", entity: "", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Alert.OverloadCount(tc.entity); got != tc.want {
				t.Errorf("OverloadCount(%q) = %d, want %d", tc.entity, got, tc.want)
			}
			if got, want := f.Alert.HasOverload(tc.entity), tc.want > 0; got != want {
				t.Errorf("HasOverload(%q) = %v, want %v", tc.entity, got, want)
			}
		})
	}
}

func TestAlertsOverloadIgnoresNonStringMetric(t *testing.T) {
	// additional_information is free-form JSON, so "metric" can arrive as a
	// number from a badly behaved producer. That is not an overload; it must
	// not panic either.
	snap := factsSnapshot()
	snap.Input.Alerts = []analysis.Alert{{
		ID:                    "a",
		SourcePath:            "ims.vdu_sb_logic.vnfc_sb_logic_1",
		ProbableCause:         "THRESHOLD_CROSSING",
		AdditionalInformation: map[string]any{"metric": 42},
	}}

	if got := NewFacts(snap).Alert.OverloadCount("ims.vdu_sb_logic"); got != 0 {
		t.Errorf("OverloadCount = %d, want 0", got)
	}
}

// --- vdu -----------------------------------------------------------------

func TestVDUReplicaFacts(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name           string
		path           string
		ready, desired int
		degraded       bool
	}{
		{
			name: "one of three ready", path: "ims.vdu_sb_logic",
			ready: 1, desired: 3, degraded: true,
		},
		{
			name: "status compared case insensitively", path: "ims.vdu_healthy",
			ready: 1, desired: 1, degraded: false,
		},
		{
			// Scaled to zero on purpose. Reporting it degraded would make every
			// deliberate scale-down look like an incident.
			name: "scaled to zero is not degraded", path: "ims.vdu_scaled_to_zero",
			ready: 0, desired: 0, degraded: false,
		},
		{
			// The engine says nothing about entities it was not given.
			name: "absent vdu is not degraded", path: "ims.vdu_missing",
			ready: 0, desired: 0, degraded: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Vdu.ReadyReplicas(tc.path); got != tc.ready {
				t.Errorf("ReadyReplicas(%q) = %d, want %d", tc.path, got, tc.ready)
			}
			if got := f.Vdu.DesiredReplicas(tc.path); got != tc.desired {
				t.Errorf("DesiredReplicas(%q) = %d, want %d", tc.path, got, tc.desired)
			}
			if got := f.Vdu.IsDegraded(tc.path); got != tc.degraded {
				t.Errorf("IsDegraded(%q) = %v, want %v", tc.path, got, tc.degraded)
			}
		})
	}
}

func TestVDUIgnoresVNFCsWithoutOwner(t *testing.T) {
	// Containment comes from VNFC.VduPath. A VNFC whose path sits under the
	// VDU but whose VDUPath is empty is not counted: the snapshot is the
	// authority on containment, and re-deriving it from the path here would be
	// a second answer that could disagree.
	snap := analysis.ContextSnapshot{
		Status: analysis.StatusComplete,
		VDUs:   []analysis.VDU{{Path: "ims.vdu_a", Replicas: 1}},
		VNFCs:  []analysis.VNFC{{Path: "ims.vdu_a.vnfc_1", Status: "RUNNING"}},
	}
	f := NewFacts(snap)

	if got := f.Vdu.ReadyReplicas("ims.vdu_a"); got != 0 {
		t.Errorf("ReadyReplicas = %d, want 0", got)
	}
	if !f.Vdu.IsDegraded("ims.vdu_a") {
		t.Error("IsDegraded = false, want true")
	}
}

// --- vnfc ----------------------------------------------------------------

func TestVNFCStatus(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name       string
		path       string
		wantStatus string
		wantDown   bool
	}{
		{"running", "ims.vdu_sb_logic.vnfc_sb_logic_1", "RUNNING", false},
		{"terminated", "ims.vdu_sb_logic.vnfc_sb_logic_2", "TERMINATED", true},
		{
			// UNKNOWN means the platform could not tell. Blaming an entity on
			// that basis is blaming it for a gap in our own data.
			name: "unknown is not down", path: "ims.vdu_sb_logic.vnfc_sb_logic_3",
			wantStatus: "UNKNOWN", wantDown: false,
		},
		{"missing is not down", "ims.nothing", "", false},
		{"case insensitive", "ims.vdu_healthy.vnfc_healthy_1", "running", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Vnfc.Status(tc.path); got != tc.wantStatus {
				t.Errorf("Status(%q) = %q, want %q", tc.path, got, tc.wantStatus)
			}
			if got := f.Vnfc.IsDown(tc.path); got != tc.wantDown {
				t.Errorf("IsDown(%q) = %v, want %v", tc.path, got, tc.wantDown)
			}
		})
	}
}

func TestVNFCTerminatedIsCaseInsensitive(t *testing.T) {
	snap := analysis.ContextSnapshot{
		VNFCs: []analysis.VNFC{{Path: "ims.a", Status: "terminated"}},
	}
	if !NewFacts(snap).Vnfc.IsDown("ims.a") {
		t.Error("IsDown = false, want true for lower-case terminated")
	}
}

func TestVNFCParent(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name, path, want string
	}{
		{"instance", "ims.vdu_sb_logic.vnfc_sb_logic_1", "ims.vdu_sb_logic"},
		{"another vdu", "ims.vdu_healthy.vnfc_healthy_1", "ims.vdu_healthy"},
		{"missing vnfc", "ims.nothing.vnfc_1", ""},
		{
			// A VDU is not a VNFC, so it has no VNFC parent.
			name: "a vdu path is not in the vnfc index", path: "ims.vdu_sb_logic", want: "",
		},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Vnfc.Parent(tc.path); got != tc.want {
				t.Errorf("Parent(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestVNFCDownPathsInVDU(t *testing.T) {
	f := testFacts(t)
	paths := f.Vnfc.DownPathsInVDU("ims.vdu_sb_logic")
	want := []string{"ims.vdu_sb_logic.vnfc_sb_logic_2"}
	if len(paths) != len(want) || paths[0] != want[0] {
		t.Fatalf("DownPathsInVDU = %v, want %v", paths, want)
	}
	if !f.Vnfc.HasAnyDownInVDU("ims.vdu_sb_logic") {
		t.Error("HasAnyDownInVDU = false")
	}
	if f.Vnfc.HasAnyDownInVDU("ims.vdu_healthy") || len(f.Vnfc.DownPathsInVDU("ims.missing")) != 0 {
		t.Error("healthy or missing VDU reported a down VNFC")
	}

	// The returned slice is caller-owned.
	paths[0] = "mutated"
	if got := f.Vnfc.DownPathsInVDU("ims.vdu_sb_logic")[0]; got != want[0] {
		t.Errorf("mutating DownPathsInVDU reached facts: %q", got)
	}
}

func TestVNFCParentComesFromTheSnapshotNotThePath(t *testing.T) {
	// Containment is what the Context Builder recorded, not what the path
	// looks like. Trimming the last label would be a second answer to a
	// question the snapshot already answers, and this fixture is what tells
	// the two apart.
	snap := analysis.ContextSnapshot{
		VNFCs: []analysis.VNFC{{Path: "ims.vdu_a.vnfc_1", VDUPath: "ims.vdu_elsewhere"}},
	}
	if got := NewFacts(snap).Vnfc.Parent("ims.vdu_a.vnfc_1"); got != "ims.vdu_elsewhere" {
		t.Errorf("Parent = %q, want the recorded VDUPath", got)
	}
}

// --- link ----------------------------------------------------------------

func TestLinkStatus(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name       string
		src, dst   string
		wantStatus string
		wantDown   bool
	}{
		{"down", "ims.a", "ims.b", "DOWN", true},
		{
			// The link is directed: the reverse edge is a different row and is
			// UP, so asking about it must not answer with the forward one.
			name: "reverse direction is a different link", src: "ims.b", dst: "ims.a",
			wantStatus: "UP", wantDown: false,
		},
		{"degraded counts as down", "ims.c", "ims.d", "degraded", true},
		{"unknown is not down", "ims.e", "ims.f", "UNKNOWN", false},
		{"missing is not down", "ims.y", "ims.z", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Link.Status(tc.src, tc.dst); got != tc.wantStatus {
				t.Errorf("Status(%q,%q) = %q, want %q", tc.src, tc.dst, got, tc.wantStatus)
			}
			if got := f.Link.IsDown(tc.src, tc.dst); got != tc.wantDown {
				t.Errorf("IsDown(%q,%q) = %v, want %v", tc.src, tc.dst, got, tc.wantDown)
			}
		})
	}
}

// severedSnapshot carries link sets between VDU pairs that differ only in how
// many of their edges are usable, which is the only thing the quantified link
// facts are about.
func severedSnapshot() analysis.ContextSnapshot {
	return analysis.ContextSnapshot{
		Status: analysis.StatusComplete,
		Links: []analysis.Link{
			// Every edge unusable, across two source instances.
			{SrcPath: "ims.vdu_src.vnfc_src_1", DstPath: "ims.vdu_gone.vnfc_gone_1", Status: "DOWN"},
			{SrcPath: "ims.vdu_src.vnfc_src_2", DstPath: "ims.vdu_gone.vnfc_gone_1", Status: "degraded"},

			// One instance of the destination is cut off, the other is not.
			{SrcPath: "ims.vdu_src.vnfc_src_1", DstPath: "ims.vdu_mixed.vnfc_mixed_1", Status: "DOWN"},
			{SrcPath: "ims.vdu_src.vnfc_src_2", DstPath: "ims.vdu_mixed.vnfc_mixed_1", Status: "DOWN"},
			{SrcPath: "ims.vdu_src.vnfc_src_1", DstPath: "ims.vdu_mixed.vnfc_mixed_2", Status: "UP"},

			// Status the platform could not determine.
			{SrcPath: "ims.vdu_src.vnfc_src_1", DstPath: "ims.vdu_murky.vnfc_murky_1", Status: "UNKNOWN"},

			// A destination VDU whose name is a prefix of another one.
			{SrcPath: "ims.vdu_src.vnfc_src_1", DstPath: "ims.vdu_gone_backup.vnfc_1", Status: "UP"},
		},
	}
}

func TestLinkIsSeveredBetween(t *testing.T) {
	f := NewFacts(severedSnapshot())

	tests := []struct {
		name, src, dst string
		want           bool
	}{
		{"every edge unusable", "ims.vdu_src", "ims.vdu_gone", true},
		{"exact source VNFC with unusable edge", "ims.vdu_src.vnfc_src_1", "ims.vdu_gone", true},
		{
			// The whole reason the quantifier is "every" and not "any": one
			// live path out of three means traffic still flows, and the more
			// the VDUs scale out the more often "any" would be satisfied.
			name: "one edge still up", src: "ims.vdu_src", dst: "ims.vdu_mixed", want: false,
		},
		{
			name: "exact source VNFC with one live edge", src: "ims.vdu_src.vnfc_src_1", dst: "ims.vdu_mixed", want: false,
		},
		{
			// The VDU has a live path through src_1, but src_2 itself has only
			// an unusable edge. Passing a VNFC must not include its sibling.
			name: "exact source excludes sibling VNFC", src: "ims.vdu_src.vnfc_src_2", dst: "ims.vdu_mixed", want: true,
		},
		{
			// Vacuous truth is the trap. No edges means the snapshot was never
			// given the topology, and that must not read as an outage.
			name: "no edges at all", src: "ims.vdu_src", dst: "ims.vdu_absent", want: false,
		},
		{"unknown is not evidence", "ims.vdu_src", "ims.vdu_murky", false},
		{
			// The prefix trap: vdu_gone must not swallow vdu_gone_backup, whose
			// only edge is UP.
			name: "sibling vdu sharing a prefix", src: "ims.vdu_src", dst: "ims.vdu_gone_backup", want: false,
		},
		{"empty arguments", "", "ims.vdu_gone", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Link.IsSeveredBetween(tc.src, tc.dst); got != tc.want {
				t.Errorf("IsSeveredBetween(%q,%q) = %v, want %v", tc.src, tc.dst, got, tc.want)
			}
		})
	}
}

func TestLinkIsSeveredTo(t *testing.T) {
	f := NewFacts(severedSnapshot())

	tests := []struct {
		name, src, dst string
		want           bool
	}{
		{
			// This is the per-instance form: mixed_1 is cut off from every
			// source even though its VDU as a whole is still reachable.
			name: "instance cut off inside a reachable vdu",
			src:  "ims.vdu_src", dst: "ims.vdu_mixed.vnfc_mixed_1", want: true,
		},
		{
			name: "sibling instance still reachable",
			src:  "ims.vdu_src", dst: "ims.vdu_mixed.vnfc_mixed_2", want: false,
		},
		{"no edges to this instance", "ims.vdu_src", "ims.vdu_mixed.vnfc_mixed_3", false},
		{"empty destination", "ims.vdu_src", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Link.IsSeveredTo(tc.src, tc.dst); got != tc.want {
				t.Errorf("IsSeveredTo(%q,%q) = %v, want %v", tc.src, tc.dst, got, tc.want)
			}
		})
	}
}

func TestLinkDownCountBetween(t *testing.T) {
	f := NewFacts(severedSnapshot())

	tests := []struct {
		name, src, dst string
		want           int
	}{
		{"all unusable", "ims.vdu_src", "ims.vdu_gone", 2},
		{"exact source VNFC", "ims.vdu_src.vnfc_src_1", "ims.vdu_gone", 1},
		{"two of three", "ims.vdu_src", "ims.vdu_mixed", 2},
		{"unknown is not counted", "ims.vdu_src", "ims.vdu_murky", 0},
		{"no edges", "ims.vdu_src", "ims.vdu_absent", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Link.DownCountBetween(tc.src, tc.dst); got != tc.want {
				t.Errorf("DownCountBetween(%q,%q) = %d, want %d", tc.src, tc.dst, got, tc.want)
			}
		})
	}
}

// --- configuration -------------------------------------------------------

func TestConfigurationHas(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name      string
		path, key string
		want      bool
	}{
		{"number", "ims.x", "number", true},
		{
			// JSON null is what the API answered, which is a different fact
			// from not having been able to ask.
			name: "json null is present", path: "ims.x", key: "nothing", want: true,
		},
		{"unknown key", "ims.x", "absent", false},
		{"unknown path", "ims.other", "number", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Configuration.Has(tc.path, tc.key); got != tc.want {
				t.Errorf("Has(%q,%q) = %v, want %v", tc.path, tc.key, got, tc.want)
			}
		})
	}
}

func TestConfigurationGetters(t *testing.T) {
	f := testFacts(t)

	tests := []struct {
		name       string
		key        string
		wantFloat  float64
		wantString string
	}{
		{"json number", "number", 5, ""},
		{"go int", "goint", 7, ""},
		{"string", "text", 0, "enabled"},
		{
			// A real zero and a missing entry are the same value here, which
			// is exactly why a rule must call Has first.
			name: "real zero", key: "zero", wantFloat: 0, wantString: "",
		},
		{"json null", "nothing", 0, ""},
		{"bool is neither", "flag", 0, ""},
		{"missing", "absent", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Configuration.GetFloat("ims.x", tc.key); got != tc.wantFloat {
				t.Errorf("GetFloat(%q) = %v, want %v", tc.key, got, tc.wantFloat)
			}
			if got := f.Configuration.GetString("ims.x", tc.key); got != tc.wantString {
				t.Errorf("GetString(%q) = %q, want %q", tc.key, got, tc.wantString)
			}
		})
	}
}

func TestConfigurationDoesNotStringifyNumbers(t *testing.T) {
	// A rule comparing against "5" when the API returned 5 has a bug.
	// Converting quietly would hide it behind a condition that never matches.
	f := testFacts(t)
	if got := f.Configuration.GetString("ims.x", "number"); got != "" {
		t.Errorf("GetString on a number = %q, want \"\"", got)
	}
	if got := f.Configuration.GetFloat("ims.x", "text"); got != 0 {
		t.Errorf("GetFloat on a string = %v, want 0", got)
	}
}

// --- immutability --------------------------------------------------------

func TestFactsDoNotShareStateAcrossInstances(t *testing.T) {
	// Facts are rebuilt per analysis but the snapshot is shared. Mutating what
	// one Facts returns must not reach another.
	snap := factsSnapshot()
	first := NewFacts(snap)

	snap.VDUs[0].Replicas = 99

	if got := first.Vdu.DesiredReplicas("ims.vdu_sb_logic"); got != 3 {
		t.Errorf("DesiredReplicas after mutating the source slice = %d, want 3", got)
	}
}
