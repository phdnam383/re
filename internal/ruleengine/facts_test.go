package ruleengine

import (
	"math"
	"testing"

	"re/internal/analysis"
)

// numericEntries maps a config key to the raw JSON-typed value it should carry
// in the snapshot. We exercise several underlying Go types because GetFloat
// accepts float32/float64 and int/int32/int64 from JSON unmarshalling.
func numericEntries() map[string]any {
	return map[string]any{
		cfgKeyLogFileCount: float64(5),
		cfgKeyLogFileSize:  float64(12.5),
		cfgKeyMemoryLimit:  float64(2000),
	}
}

func cfgSnapshot(path string, entries map[string]any) analysis.ContextSnapshot {
	out := analysis.ConfigurationEntry{}
	snap := analysis.ContextSnapshot{
		Input:         analysis.ContextInput{Alerts: []analysis.Alert{{SourcePath: path}}},
		Configuration: make([]analysis.ConfigurationEntry, 0, len(entries)),
	}
	for key, val := range entries {
		out.Key = key
		out.Value = val
		snap.Configuration = append(snap.Configuration, out)
	}
	return snap
}

func TestAlertVduPath(t *testing.T) {
	const (
		vduPath  = "ims.vdu_sb_sip_core"
		vnfcPath = "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1"
	)
	base := analysis.ContextSnapshot{
		VDUs:  []analysis.VDU{{Path: vduPath, Instances: 2}},
		VNFCs: []analysis.VNFC{{Path: vnfcPath, VDUPath: vduPath, Status: statusRunning}},
	}

	alertWith := func(source string) analysis.ContextSnapshot {
		s := base
		s.Input.Alerts = []analysis.Alert{{SourcePath: source}}
		return s
	}

	t.Run("source is a VNFC path -> parent VDU", func(t *testing.T) {
		if got := NewFacts(alertWith(vnfcPath)).Alert.VduPath(); got != vduPath {
			t.Errorf("VduPath() = %q, want %q", got, vduPath)
		}
	})

	t.Run("source is a VDU path -> itself", func(t *testing.T) {
		if got := NewFacts(alertWith(vduPath)).Alert.VduPath(); got != vduPath {
			t.Errorf("VduPath() = %q, want %q", got, vduPath)
		}
	})

	t.Run("source path not in snapshot -> empty", func(t *testing.T) {
		if got := NewFacts(alertWith("ims.vdu_sb_unknown.vnfc_unknown_1")).Alert.VduPath(); got != "" {
			t.Errorf("VduPath() = %q, want empty", got)
		}
	})

	t.Run("no alert -> empty", func(t *testing.T) {
		if got := NewFacts(base).Alert.VduPath(); got != "" {
			t.Errorf("VduPath() with no alert = %q, want empty", got)
		}
	})
}

func TestConfigurationFacts(t *testing.T) {
	const path = "ims.vdu_sb_logic.vnfc_sb_logic_1"
	f := NewFacts(cfgSnapshot(path, numericEntries()))

	t.Run("getters read their keys", func(t *testing.T) {
		if got := f.Cfg.LogFileCount(path); got != 5 {
			t.Errorf("LogFileCount = %v, want 5", got)
		}
		if got := f.Cfg.LogFileSizeMB(path); got != 12.5 {
			t.Errorf("LogFileSizeMB = %v, want 12.5", got)
		}
		if got := f.Cfg.MemoryLimitMB(path); got != 2000 {
			t.Errorf("MemoryLimitMB = %v, want 2000", got)
		}
	})

	t.Run("LogFootprintRatio = count*size/limit", func(t *testing.T) {
		got := f.Cfg.LogFootprintRatio(path)
		if want := 5 * 12.5 / 2000; math.Abs(got-want) > 1e-9 {
			t.Errorf("LogFootprintRatio = %v, want %v", got, want)
		}
	})

	t.Run("missing entry yields zero numerics", func(t *testing.T) {
		const absent = "ims.vdu_sb_logic.vnfc_sb_logic_9"
		// Same VDU, different VNFC path -> no entries at all.
		if got := f.Cfg.LogFileCount(absent); got != 0 {
			t.Errorf("LogFileCount(absent) = %v, want 0", got)
		}
		if got := f.Cfg.LogFootprintRatio(absent); got != 0 {
			t.Errorf("LogFootprintRatio(absent) = %v, want 0", got)
		}
	})

	t.Run("zero memory limit avoids division by zero", func(t *testing.T) {
		f := NewFacts(cfgSnapshot(path, map[string]any{
			cfgKeyLogFileCount: float64(5),
			cfgKeyLogFileSize:  float64(12.5),
			cfgKeyMemoryLimit:  float64(0),
		}))
		if got := f.Cfg.LogFootprintRatio(path); got != 0 {
			t.Errorf("LogFootprintRatio with 0 limit = %v, want 0", got)
		}
	})
}

func TestConfigurationFactsIsLogVerbose(t *testing.T) {
	const path = "ims.vdu_sb_logic.vnfc_sb_logic_1"
	levels := map[string]bool{
		"DEBUG": true,
		"debug": true,
		"TRACE": true,
		"INFO":  false,
		"":      false,
	}
	for level, want := range levels {
		t.Run("level_"+level, func(t *testing.T) {
			f := NewFacts(cfgSnapshot(path, map[string]any{cfgKeyLogLevel: level}))
			if got := f.Cfg.LogLevel(path); got != level {
				t.Errorf("LogLevel(%q) = %q, want %q", level, got, level)
			}
			if got := f.Cfg.IsLogVerbose(path); got != want {
				t.Errorf("IsLogVerbose(%q) = %v, want %v", level, got, want)
			}
		})
	}

	// A numeric log_level is not a string -> not verbose, LogLevel returns "".
	f := NewFacts(cfgSnapshot(path, map[string]any{cfgKeyLogLevel: float64(7)}))
	if got := f.Cfg.LogLevel(path); got != "" {
		t.Errorf("LogLevel(numeric) = %q, want empty", got)
	}
	if got := f.Cfg.IsLogVerbose(path); got != false {
		t.Errorf("IsLogVerbose(numeric) = %v, want false", got)
	}

	// Missing entry -> empty level, not verbose.
	const absent = "ims.vdu_sb_logic.vnfc_sb_logic_9"
	if got := f.Cfg.LogLevel(absent); got != "" {
		t.Errorf("LogLevel(absent) = %q, want empty", got)
	}
	if got := f.Cfg.IsLogVerbose(absent); got != false {
		t.Errorf("IsLogVerbose(absent) = %v, want false", got)
	}
}

// metricSnapshot builds a snapshot whose alerts carry each metric's
// observed/threshold in additional_information — the shape the rule-engine
// view reads directly. Every metric here is attributed to the same source path.
func metricSnapshot(path string, metrics map[string][2]float64) analysis.ContextSnapshot {
	snap := analysis.ContextSnapshot{}
	for metric, pair := range metrics {
		snap.Input.Alerts = append(snap.Input.Alerts, analysis.Alert{
			SourcePath: path,
			AdditionalInformation: map[string]any{
				"metric":          metric,
				"observed_value":  pair[0],
				"threshold_value": pair[1],
			},
		})
	}
	return snap
}

func TestErlFacts(t *testing.T) {
	const path = "ims.vdu_sb_logic.vnfc_sb_logic_1"
	f := NewFacts(metricSnapshot(path, map[string][2]float64{
		metricErlProcess: {90, 100},
		metricErlPort:    {5, 200}, // below threshold, still a ratio
		metricErlAtom:    {3, 0},   // zero threshold -> 0
	}))

	cases := map[string]struct {
		got  float64
		want float64
	}{
		"ProcessRatio": {f.Erl.ProcessRatio(path), 0.9},
		"PortRatio":    {f.Erl.PortRatio(path), 0.025},
		"AtomRatio":    {f.Erl.AtomRatio(path), 0},
	}
	for name, c := range cases {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}

	// Missing metric (path or name) resolves to 0.
	if got := f.Erl.ProcessRatio("ims.vdu_sb_logic.vnfc_sb_logic_9"); got != 0 {
		t.Errorf("ProcessRatio(absent) = %v, want 0", got)
	}
}

func TestTableFacts(t *testing.T) {
	const (
		path      = "ims.vdu_cs_logic.vnfc_cs_logic_1"
		garbageTb = "transaction_garbage_timer"
		tcpTb     = "tcp_conns_management_tb_tmp"
	)
	// 90/100 rows -> ratio 0.9; purge 10 to reach the 0.8 line (80 rows).
	f := NewFacts(metricSnapshot(path, map[string][2]float64{
		garbageTb: {90, 100},
	}))

	if got := f.Table.FillRatio(path, garbageTb); math.Abs(got-0.9) > 1e-9 {
		t.Errorf("FillRatio = %v, want 0.9", got)
	}
	if got, want := f.Table.RowsAbove(path, garbageTb, 0.8), 10; got != want {
		t.Errorf("RowsAbove = %v, want %v", got, want)
	}

	// Missing table: FillRatio 0, RowsAbove 0 (nothing to purge).
	if got := f.Table.FillRatio(path, tcpTb); got != 0 {
		t.Errorf("FillRatio(absent) = %v, want 0", got)
	}
	if got := f.Table.RowsAbove(path, tcpTb, 0.8); got != 0 {
		t.Errorf("RowsAbove(absent) = %v, want 0", got)
	}

	// Table below the requested ratio -> 0 (not negative).
	const below = "ims.vdu_cs_logic.vnfc_cs_logic_2"
	f = NewFacts(metricSnapshot(below, map[string][2]float64{garbageTb: {50, 100}}))
	if got := f.Table.RowsAbove(below, garbageTb, 0.8); got != 0 {
		t.Errorf("RowsBelow = %v, want 0", got)
	}
}

func TestLinkFacts(t *testing.T) {
	const target = "remote.sipa_peer.peer_1"
	snap := analysis.ContextSnapshot{Links: []analysis.LinkEntry{
		{Target: target, Status: linkStatusFailed},
	}}
	f := NewFacts(snap)

	if !f.Link.PingFails(target) {
		t.Error("PingFails = false, want true")
	}
	if f.Link.PingAnswers(target) {
		t.Error("PingAnswers = true, want false")
	}

	t.Run("successful ping answers, does not fail", func(t *testing.T) {
		f := NewFacts(analysis.ContextSnapshot{Links: []analysis.LinkEntry{
			{Target: target, Status: linkStatusSuccess},
		}})
		if !f.Link.PingAnswers(target) {
			t.Error("PingAnswers = false, want true")
		}
		if f.Link.PingFails(target) {
			t.Error("PingFails = true, want false")
		}
	})

	// A probe not in the snapshot makes neither variant true — "we did not
	// probe" is not "the peer answered".
	t.Run("absent probe is neither failure nor answer", func(t *testing.T) {
		const absent = "remote.sipa_peer.peer_9"
		if f.Link.PingFails(absent) {
			t.Error("PingFails(absent) = true, want false")
		}
		if f.Link.PingAnswers(absent) {
			t.Error("PingAnswers(absent) = true, want false")
		}
	})

	t.Run("automatic remote probe failed", func(t *testing.T) {
		f := NewFacts(analysis.ContextSnapshot{
			Input: analysis.ContextInput{Alerts: []analysis.Alert{{
				AdditionalInformation: map[string]any{
					analysis.AdditionalInformationRemoteIP: target,
				},
			}}},
			Links: []analysis.LinkEntry{{Target: target, Status: linkStatusFailed}},
		})
		if !f.Link.PingToRemoteFailed() {
			t.Error("PingToRemoteFailed = false, want true")
		}
		if f.Link.PingToRemoteSuccess() {
			t.Error("PingToRemoteSuccess = true, want false")
		}
	})

	t.Run("automatic remote probe succeeded", func(t *testing.T) {
		f := NewFacts(analysis.ContextSnapshot{
			Input: analysis.ContextInput{Alerts: []analysis.Alert{{
				AdditionalInformation: map[string]any{
					analysis.AdditionalInformationRemoteIP: target,
				},
			}}},
			Links: []analysis.LinkEntry{{Target: target, Status: linkStatusSuccess}},
		})
		if !f.Link.PingToRemoteSuccess() {
			t.Error("PingToRemoteSuccess = false, want true")
		}
		if f.Link.PingToRemoteFailed() {
			t.Error("PingToRemoteFailed = true, want false")
		}
	})

	t.Run("missing automatic remote probe is unknown", func(t *testing.T) {
		f := NewFacts(analysis.ContextSnapshot{Input: analysis.ContextInput{Alerts: []analysis.Alert{{
			AdditionalInformation: map[string]any{
				analysis.AdditionalInformationRemoteIP: target,
			},
		}}}})
		if f.Link.PingToRemoteSuccess() || f.Link.PingToRemoteFailed() {
			t.Error("missing automatic probe must make both predicates false")
		}
	})
}
