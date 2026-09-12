package ruleengine

import (
	"strings"

	"re/internal/analysis"
)

type metricKey struct{ path, metric string }

type view struct {
	snap analysis.ContextSnapshot

	vduByPath    map[string]analysis.VDU
	vnfcByPath   map[string]analysis.VNFC
	vnfcsByVDU   map[string][]analysis.VNFC
	configByKey  map[string]analysis.ConfigurationEntry
	metricPair   map[metricKey][2]float64
	metricByName map[string]analysis.MetricEntry
	linkByPath   map[string]analysis.LinkEntry
}

func newView(snap analysis.ContextSnapshot) *view {
	v := &view{
		snap:         snap,
		vduByPath:    make(map[string]analysis.VDU, len(snap.VDUs)),
		vnfcByPath:   make(map[string]analysis.VNFC, len(snap.VNFCs)),
		vnfcsByVDU:   make(map[string][]analysis.VNFC, len(snap.VDUs)),
		configByKey:  make(map[string]analysis.ConfigurationEntry, len(snap.Configuration)),
		metricPair:   make(map[metricKey][2]float64, len(snap.Input.Alerts)),
		metricByName: make(map[string]analysis.MetricEntry, len(snap.Metrics)),
		linkByPath:   make(map[string]analysis.LinkEntry, len(snap.Links)),
	}

	for _, d := range snap.VDUs {
		v.vduByPath[d.Path] = d
	}
	for _, n := range snap.VNFCs {
		v.vnfcByPath[n.Path] = n

		if n.VDUPath != "" {
			v.vnfcsByVDU[n.VDUPath] = append(v.vnfcsByVDU[n.VDUPath], n)
		}
	}
	for _, e := range snap.Configuration {
		v.configByKey[e.Key] = e
	}
	for _, a := range snap.Input.Alerts {
		name, ok := a.AdditionalInformation["metric"].(string)
		if !ok {
			continue
		}
		key := metricKey{path: a.SourcePath, metric: name}
		if _, seen := v.metricPair[key]; seen {
			continue
		}
		observed, _ := asFloat(a.AdditionalInformation["observed_value"])
		threshold, _ := asFloat(a.AdditionalInformation["threshold_value"])
		v.metricPair[key] = [2]float64{observed, threshold}
	}
	for _, l := range snap.Links {
		v.linkByPath[l.Target] = l
	}
	for _, m := range snap.Metrics {
		v.metricByName[m.Name] = m
	}
	return v
}

func (v *view) metricEntry(name string) (analysis.MetricEntry, bool) {
	e, ok := v.metricByName[name]
	return e, ok
}

func (v *view) metric(path, metric string) ([2]float64, bool) {
	pair, ok := v.metricPair[metricKey{path: path, metric: metric}]
	return pair, ok
}

func (v *view) link(target string) (analysis.LinkEntry, bool) {
	p, ok := v.linkByPath[target]
	return p, ok
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func (v *view) alertsUnder(entity string) []analysis.Alert {
	if entity == "" {
		return nil
	}
	var out []analysis.Alert
	for _, a := range v.snap.Input.Alerts {
		if a.SourcePath == entity || strings.HasPrefix(a.SourcePath, entity+".") {
			out = append(out, a)
		}
	}
	return out
}

func (v *view) readyReplicas(vduPath string) int {
	ready := 0
	for _, n := range v.vnfcsByVDU[vduPath] {
		if equalFold(n.Status, statusRunning) {
			ready++
		}
	}
	return ready
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
