package ruleengine

import (
	"strings"

	"re/internal/analysis"
)

// Statuses a rule reasons about. They are compared case-insensitively, so a
// snapshot carrying "running" answers the same as one carrying "RUNNING".
const (
	statusRunning    = "RUNNING"
	statusTerminated = "TERMINATED"

	linkDown     = "DOWN"
	linkDegraded = "DEGRADED"
)

// causeThresholdCrossing is the probable cause an overload alert carries, and
// overloadMetricPrefix is what its "metric" starts with. Both are part of the
// 3GPP alert vocabulary the ingest side already speaks, so they are matched
// here rather than restated in every rule.
const (
	causeThresholdCrossing = "THRESHOLD_CROSSING"
	overloadMetricPrefix   = "overload"
)

// Facts is the read-only API a rule sees, bound into GRL under the name "Ctx".
//
// Its shape is dictated by what a reflection-driven rule language can express:
//
//   - exported types and methods, because the runtime binds them by name;
//   - single return values, because a GRL expression cannot destructure a
//     (value, ok) pair — absence is reported by a Has* predicate or by a zero
//     value instead;
//   - no side effects, no I/O, no mutation. A fact object must never hold a
//     database handle or an HTTP client: rule content is data loaded from
//     PostgreSQL, and a rule that could reach outside the snapshot would turn
//     a row edit into arbitrary I/O.
type Facts struct {
	Alert         *AlertFacts
	Vdu           *VDUFacts
	Vnfc          *VNFCFacts
	Link          *LinkFacts
	Configuration *ConfigurationFacts

	// v is the shared index, unexported so a runtime binding by reflection
	// sees only the fact groups above.
	v *view
}

// NewFacts builds the fact API over one snapshot, indexing it once.
func NewFacts(snap analysis.ContextSnapshot) *Facts {
	v := newView(snap)
	return &Facts{
		Alert:         &AlertFacts{v: v},
		Vdu:           &VDUFacts{v: v},
		Vnfc:          &VNFCFacts{v: v},
		Link:          &LinkFacts{v: v},
		Configuration: &ConfigurationFacts{v: v},
		v:             v,
	}
}

// --- alerts --------------------------------------------------------------

// AlertFacts answers questions about the alerts that opened the incident.
type AlertFacts struct{ v *view }

// HasCause reports whether the request's alert carries this probable cause.
func (f *AlertFacts) HasCause(cause string) bool {
	for _, alert := range f.v.snap.Input.Alerts {
		if equalFold(alert.ProbableCause, cause) {
			return true
		}
	}
	return false
}

// SourcePath is the managed-object path that raised the request's alert.
func (f *AlertFacts) SourcePath() string { return f.alert().SourcePath }

func (f *AlertFacts) alert() analysis.Alert {
	if len(f.v.snap.Input.Alerts) == 0 {
		return analysis.Alert{}
	}
	return f.v.snap.Input.Alerts[0]
}

// HasOverload reports whether the entity, or anything below it, raised an
// overload alert.
//
// An overload alert is a THRESHOLD_CROSSING whose additional_information names
// a metric starting with "overload" — overload_ram, overload_cpu, and whatever
// the ingest side adds next. Matching on the prefix rather than on a fixed list
// means a new overload metric does not need an engine change to be understood
// as overload.
func (f *AlertFacts) HasOverload(entity string) bool { return f.OverloadCount(entity) > 0 }

// OverloadCount is how many overload alerts the entity or its descendants
// raised.
//
// It exists as a separate call because GRL has neither len() nor a conditional
// in "then": a rule that should only fire once the load is sustained has to say
// so in its "when", which means the count has to be an expression.
func (f *AlertFacts) OverloadCount(entity string) int {
	count := 0
	for _, a := range f.v.alertsUnder(entity) {
		if !equalFold(a.ProbableCause, causeThresholdCrossing) {
			continue
		}
		metric, ok := a.AdditionalInformation["metric"].(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.ToLower(metric), overloadMetricPrefix) {
			count++
		}
	}
	return count
}

// --- vdu -----------------------------------------------------------------

// VDUFacts answers questions about deployment units.
type VDUFacts struct{ v *view }

// DesiredReplicas is the replica count the VDU declares, or 0 when the VDU is
// not in the snapshot.
func (f *VDUFacts) DesiredReplicas(path string) int { return f.v.vduByPath[path].Replicas }

// ReadyReplicas is how many of the VDU's VNFCs are RUNNING.
func (f *VDUFacts) ReadyReplicas(path string) int { return f.v.readyReplicas(path) }

// IsDegraded reports whether the VDU is running fewer replicas than it wants.
//
// The desired count must be positive: a VDU declaring 0 replicas is scaled to
// zero deliberately, and reporting it as degraded would make every intentional
// scale-down look like an incident. A VDU missing from the snapshot has a
// desired count of 0 and so is not degraded either — the rule engine says
// nothing about entities it was not given, which is what keeps a partial
// context from inventing failures.
func (f *VDUFacts) IsDegraded(path string) bool {
	desired := f.DesiredReplicas(path)
	return desired > 0 && f.ReadyReplicas(path) < desired
}

// --- vnfc ----------------------------------------------------------------

// VNFCFacts answers questions about individual component instances.
type VNFCFacts struct{ v *view }

// Status is the VNFC's status, or "" when it is not in the snapshot.
func (f *VNFCFacts) Status(path string) string { return f.v.vnfcByPath[path].Status }

// IsDown reports whether the VNFC is TERMINATED.
//
// Only TERMINATED counts. UNKNOWN means the platform could not determine the
// state and a missing VNFC means the snapshot never carried it — in both cases
// the honest answer is that we do not know it is down, and a rule that blamed
// an entity on that basis would be blaming it for a gap in our own data.
func (f *VNFCFacts) IsDown(path string) bool {
	return equalFold(f.Status(path), statusTerminated)
}

// HasAnyDownInVDU reports whether a VDU owns at least one TERMINATED VNFC.
func (f *VNFCFacts) HasAnyDownInVDU(vduPath string) bool {
	return len(f.DownPathsInVDU(vduPath)) > 0
}

// DownPathsInVDU returns the deterministic paths of all TERMINATED VNFCs owned
// by a VDU. Snapshot collections are already path-sorted by the Context Builder.
func (f *VNFCFacts) DownPathsInVDU(vduPath string) []string {
	var out []string
	for _, vnfc := range f.v.vnfcsByVDU[vduPath] {
		if equalFold(vnfc.Status, statusTerminated) {
			out = append(out, vnfc.Path)
		}
	}
	return out
}

// Parent is the path of the VDU that owns this VNFC, or "" when the snapshot
// does not carry it.
//
// This lets collection-oriented rule helpers group instances by deployment
// unit without deriving containment from path strings.
//
// It returns VNFC.VDUPath as the Context Builder recorded it rather than
// trimming the last label off the path. Containment is something the snapshot
// already answers, and deriving it a second way here is how the two answers
// start to disagree.
func (f *VNFCFacts) Parent(path string) string { return f.v.vnfcByPath[path].VDUPath }

// --- link ----------------------------------------------------------------

// LinkFacts answers questions about directed links between entities.
type LinkFacts struct{ v *view }

// Status is the status of the (src, dst) link, or "" when the snapshot does
// not carry it. The link is directed: asking about (b, a) never returns the
// status of (a, b).
func (f *LinkFacts) Status(src, dst string) string {
	return f.v.linkByPair[linkKey{src: src, dst: dst}].Status
}

// IsDown reports whether the link is unusable, which covers both DOWN and
// DEGRADED.
//
// DEGRADED is included because it is a link that is losing traffic, and every
// scenario written so far treats "the peer is unreachable enough to matter" as
// one condition. A rule that needs to tell the two apart reads Status.
func (f *LinkFacts) IsDown(src, dst string) bool {
	return linkUnusable(f.Status(src, dst))
}

// IsSeveredBetween reports whether connectivity from a source to a destination
// VDU is wholly lost: at least one matching link is known, and every known one
// is unusable. The source may be a VDU (all of its VNFCs) or one exact VNFC.
//
// The quantifier is "every", not "any", and that is the whole point. A VDU pair
// has as many links as it has instance pairs, so "any link is down" gets easier
// to satisfy the more the VDUs scale out — one flapping edge out of six would
// report an outage while five paths still carry traffic, which is precisely
// what running several instances behind a load balancer is for.
//
// An empty link set answers false. "Every link in nothing is down" is
// vacuously true and would turn a snapshot that was never given the topology
// into a reported outage — the same reason VDU.IsDegraded insists on a positive
// desired count before calling anything degraded.
func (f *LinkFacts) IsSeveredBetween(srcPath, dstVDU string) bool {
	return severed(f.v.linksBetween(srcPath, dstVDU))
}

// IsSeveredTo reports whether every known link from an instance of the source
// VDU to exactly this destination instance is unusable.
//
// This is the per-destination-instance form: where IsSeveredBetween selects a
// whole destination VDU, this answers whether the selected source cannot reach
// one exact destination instance.
func (f *LinkFacts) IsSeveredTo(srcVDU, dstPath string) bool {
	return severed(f.v.linksTo(srcVDU, dstPath))
}

// DownCountBetween is how many matching links from a source VDU or VNFC to the
// destination VDU are unusable.
//
// It exists for the rule that wants a threshold rather than the absolute
// IsSeveredBetween states — GRL has no way to count a collection itself, so a
// rule that fires once half the paths are gone needs the count as an
// expression.
func (f *LinkFacts) DownCountBetween(srcPath, dstVDU string) int {
	count := 0
	for _, l := range f.v.linksBetween(srcPath, dstVDU) {
		if linkUnusable(l.Status) {
			count++
		}
	}
	return count
}

// severed reports whether a non-empty set of links is entirely unusable.
func severed(links []analysis.Link) bool {
	if len(links) == 0 {
		return false
	}
	for _, l := range links {
		if !linkUnusable(l.Status) {
			return false
		}
	}
	return true
}

// linkUnusable is the single definition of a link that cannot be relied on.
// UNKNOWN is not included: not knowing the status of a link is not evidence
// that traffic is failing over it, for the reason VNFC.IsDown gives.
func linkUnusable(status string) bool {
	return equalFold(status, linkDown) || equalFold(status, linkDegraded)
}

// --- configuration -------------------------------------------------------

// ConfigurationFacts answers questions about effective configuration values —
// what an NF is actually running, as read from its configuration API, never
// what PostgreSQL declares it should be running.
type ConfigurationFacts struct{ v *view }

// Has reports whether the value was read successfully.
//
// It is true even when the value is JSON null: null is what the API answered,
// which is different from not having been able to ask. This is the guard a rule
// must open with, because the getters below cannot distinguish a missing entry
// from a real zero.
func (f *ConfigurationFacts) Has(path, key string) bool {
	_, ok := f.v.configByKey[configKey{path: path, key: key}]
	return ok
}

// GetFloat returns a numeric configuration value, or 0 when the entry is
// missing or is not a number.
//
// JSON numbers decode as float64; the int cases are there for snapshots built
// in Go rather than decoded from a response, which is what tests do.
func (f *ConfigurationFacts) GetFloat(path, key string) float64 {
	e, ok := f.v.configByKey[configKey{path: path, key: key}]
	if !ok {
		return 0
	}
	switch n := e.Value.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// GetString returns a string configuration value, or "" when the entry is
// missing or is not a string.
//
// A number is not stringified. A rule comparing a value against "3" when the
// API returned 3 has a bug, and quietly converting would hide it behind a
// condition that silently never matches.
func (f *ConfigurationFacts) GetString(path, key string) string {
	e, ok := f.v.configByKey[configKey{path: path, key: key}]
	if !ok {
		return ""
	}
	s, _ := e.Value.(string)
	return s
}
