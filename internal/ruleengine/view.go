package ruleengine

import (
	"strings"

	"re/internal/analysis"
)

// linkKey addresses one directed link. A link is directed, so (a,b) and (b,a)
// are different edges and a rule asking about one is never answered with the
// other.
type linkKey struct{ src, dst string }

// configKey addresses one effective configuration value.
type configKey struct{ path, key string }

// view is the indexed, read-only projection of a snapshot that the fact groups
// share.
//
// The indexes are built once per analysis and reused by every rule row. A rule
// set that asks about the same VNFC forty times should not walk the slice forty
// times, and — more importantly — every rule must see exactly the same data:
// the view holds the snapshot by value and exposes nothing that can write to
// it, so the facts a rule read cannot have been changed by the rule that ran
// before it.
type view struct {
	snap analysis.ContextSnapshot

	vduByPath   map[string]analysis.VDU
	vnfcByPath  map[string]analysis.VNFC
	vnfcsByVDU  map[string][]analysis.VNFC
	linkByPair  map[linkKey]analysis.Link
	configByKey map[configKey]analysis.ConfigurationEntry
}

func newView(snap analysis.ContextSnapshot) *view {
	v := &view{
		snap:        snap,
		vduByPath:   make(map[string]analysis.VDU, len(snap.VDUs)),
		vnfcByPath:  make(map[string]analysis.VNFC, len(snap.VNFCs)),
		vnfcsByVDU:  make(map[string][]analysis.VNFC, len(snap.VDUs)),
		linkByPair:  make(map[linkKey]analysis.Link, len(snap.Links)),
		configByKey: make(map[configKey]analysis.ConfigurationEntry, len(snap.Configuration)),
	}

	for _, d := range snap.VDUs {
		v.vduByPath[d.Path] = d
	}
	for _, n := range snap.VNFCs {
		v.vnfcByPath[n.Path] = n
		// Containment comes from VNFC.VDUPath, which the Context Builder
		// filled in from the ltree subtree the row was found under. Re-deriving
		// it here by trimming the path would be a second, disagreeing answer to
		// a question the snapshot already answers.
		if n.VDUPath != "" {
			v.vnfcsByVDU[n.VDUPath] = append(v.vnfcsByVDU[n.VDUPath], n)
		}
	}
	for _, l := range snap.Links {
		v.linkByPair[linkKey{src: l.SrcPath, dst: l.DstPath}] = l
	}
	for _, e := range snap.Configuration {
		v.configByKey[configKey{path: e.Path, key: e.Key}] = e
	}
	return v
}

// alertsUnder returns the alerts raised by the entity or by anything below it
// in the managed-object tree.
//
// Descendant scoping is what makes a VDU-level rule work at all: a pod reports
// its own overload under its own path, and a rule reasoning about the VDU would
// otherwise see nothing. The prefix test is on LTREE labels — the trailing dot
// is required so "ims.vdu_sb" does not swallow "ims.vdu_sb_logic".
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

// readyReplicas counts the VNFCs under a VDU that are RUNNING.
func (v *view) readyReplicas(vduPath string) int {
	ready := 0
	for _, n := range v.vnfcsByVDU[vduPath] {
		if equalFold(n.Status, statusRunning) {
			ready++
		}
	}
	return ready
}

// equalFold is the comparison used for every enum-like value a rule matches on
// — probable cause, alert type, VNFC status, link status, metric name.
//
// LTREE paths deliberately do not go through it: PostgreSQL ltree labels are
// case-sensitive, so "ims.VDU_sb_logic" is a different node from
// "ims.vdu_sb_logic" and treating them as one would let a rule blame an entity
// that does not exist.
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
