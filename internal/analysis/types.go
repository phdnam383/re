// Package analysis owns the domain types shared across the engine: the
// request input, the context snapshot the Context Builder produces, and the
// metadata that escapes over gRPC.
//
// The dependency rule is one-directional. Implementation packages
// (internal/contextbuilder, and later the rule engine and gRPC transport)
// import analysis; analysis imports none of them. Wiring happens only in
// cmd/engine. A dependency in the other direction creates an import cycle.
//
// These types are deliberately independent of the generated protobuf: the
// transport maps pb <-> domain so a wire-format change cannot reach into the
// builder, and so the snapshot can be marshalled to JSON for golden tests.
package analysis

import "time"

// --- context status ------------------------------------------------------

// Context status values carried by ContextSnapshot.Status.
const (
	// StatusComplete means every provider with work in the merged plan
	// answered for every target it was given.
	StatusComplete = "COMPLETE"

	// StatusPartial means at least one target produced a MissingContext.
	// The snapshot is still usable — the other providers' results are kept.
	StatusPartial = "PARTIAL"
)

// --- providers -----------------------------------------------------------

// Provider names. They appear in MissingContext.Provider and they fix the
// order results are merged in, which is why they are constants rather than
// free-form strings: the merge order is part of the persisted contract, not
// an implementation detail.
const (
	ProviderVDU           = "VDU"
	ProviderLink          = "LINK"
	ProviderConfiguration = "CONFIGURATION"
)

// --- missing-context reasons ---------------------------------------------

// Reasons recorded on MissingContext. A closed vocabulary rather than a
// formatted sentence: a caller deciding whether to retry or to escalate
// should not have to parse prose, and "the row does not exist" is an
// operator's problem in a way that "the backend timed out" is not.
const (
	// ReasonNotFound — the provider ran and the target genuinely is not
	// there: no vdu row for the path, no link row for the directed pair.
	ReasonNotFound = "NOT_FOUND"

	// ReasonQueryFailed — the database query itself failed. The target may
	// or may not exist; nothing was learned about it.
	ReasonQueryFailed = "QUERY_FAILED"

	// ReasonRequestFailed — the configuration GET could not be completed
	// (dial error, connection reset, malformed URL).
	ReasonRequestFailed = "REQUEST_FAILED"

	// ReasonHTTPStatus — the configuration API answered with a non-2xx.
	ReasonHTTPStatus = "HTTP_STATUS"

	// ReasonTimeout — the per-call configuration timeout elapsed. Distinct
	// from a caller-side deadline, which aborts the whole build with
	// ctx.Err() instead of degrading to PARTIAL.
	ReasonTimeout = "TIMEOUT"

	// ReasonEmptyBody — 2xx with nothing in the body. Treated as missing
	// rather than as a null value: a configuration API that answers 200 with
	// no content has not told us what the effective value is.
	ReasonEmptyBody = "EMPTY_BODY"

	// ReasonInvalidJSON — 2xx whose body is not a single valid JSON value.
	ReasonInvalidJSON = "INVALID_JSON"
)

// --- input ---------------------------------------------------------------

// ContextInput is the validated request handed to the Context Builder and
// retained verbatim in the snapshot it produces.
//
// Incident is the caller's opaque incident identifier, mirroring
// AnalyzeIncidentRequest.incident. The engine owns no incident lifecycle, so
// it is carried for correlation and never interpreted.
type ContextInput struct {
	RequestID string  `json:"request_id"`
	Incident  string  `json:"incident"`
	Alerts    []Alert `json:"alerts"`
}

// Alert mirrors the alert row (db/schema.sql, 3GPP TS 28.111 / ITU-T X.733).
//
// AdditionalInformation holds the decoded TS 28.111 additionalInformation
// name-value pairs. Selector matching reads it, so the values keep their JSON
// types — 80 and "80" are not the same value.
type Alert struct {
	ID                    string         `json:"id"`
	SourcePath            string         `json:"source_path"`
	AlertType             string         `json:"alert_type,omitempty"`
	ProbableCause         string         `json:"probable_cause,omitempty"`
	PerceivedSeverity     string         `json:"perceived_severity,omitempty"`
	State                 string         `json:"state,omitempty"`
	CreatedAt             string         `json:"created_at,omitempty"`
	AdditionalInformation map[string]any `json:"additional_information,omitempty"`
}

// --- topology context ----------------------------------------------------

// VDU is one row of the vdu table, returned whole. The Context Builder does
// not project a subset: it cannot know which attribute a rule will read, and
// a rule that needs one more column should not require a builder change.
//
// The vdu row is returned on its own — no join to managed_object or
// vnfc_binding. Those carry tree bookkeeping and slot history, neither of
// which a rule reasons over.
type VDU struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Replicas  int    `json:"replicas"`

	// Selector is the optional Kubernetes label selector override. The column
	// is nullable and NULL reaches here as "": both mean "not overridden", and
	// the distinction has no consumer.
	Selector string `json:"selector,omitempty"`

	// NFConfig is the VDU-level configuration blob (vdu.nf_config). This is
	// the *declared* config from PostgreSQL, not the effective config an NF is
	// running — that only ever comes from the Configuration Provider.
	NFConfig map[string]any `json:"nf_config,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// VNFC is one row of the vnfc table, returned whole.
//
// VDUPath is not a column: VNFC containment is expressed by the ltree path
// (ims.vdu_<n>.vnfc_<n>), and the provider fills this in with the VDU whose
// subtree the row was found under. It is what makes the flat VDUs/VNFCs
// collections navigable without re-parsing paths in every rule.
type VNFC struct {
	Path    string `json:"path"`
	VDUPath string `json:"vdu_path"`

	// K8sUID is the current pod's metadata.uid. Empty when the pod is down or
	// pending (the column is nullable).
	K8sUID string `json:"k8s_uid,omitempty"`

	Name   string `json:"name"`
	Status string `json:"status"` // RUNNING | TERMINATED | UNKNOWN

	// InstanceConfig is the instance-level override layered on top of
	// VDU.NFConfig (vnfc.instance_config). Declared config, not effective.
	InstanceConfig map[string]any `json:"instance_config,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Link is one row of the link table, returned whole.
//
// A link is directed. The Link Provider fetches exactly the (SrcPath,
// DstPath) pairs a profile named and never the reverse pair, so a profile
// that wants both directions must name both.
//
// The port columns are nullable; NULL reaches here as 0, which is not a
// usable port number and so is unambiguous.
type Link struct {
	SrcPath string `json:"src_path"`
	DstPath string `json:"dst_path"`

	SrcIP   string `json:"src_ip,omitempty"`
	SrcPort int    `json:"src_port,omitempty"`
	DstIP   string `json:"dst_ip,omitempty"`
	DstPort int    `json:"dst_port,omitempty"`

	Protocol string `json:"protocol,omitempty"` // SIP | DIAMETER | H248 | ...
	Status   string `json:"status,omitempty"`   // UP | DOWN | DEGRADED | UNKNOWN

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- configuration context -----------------------------------------------

// ConfigurationEntry is one effective configuration value read from an
// external NF configuration API.
//
// It is keyed by (Path, Key) and carries the URL it came from, so a snapshot
// records not just what the value was but where it was read — the profile is
// the only source of that URL, and two profiles disagreeing about it is a
// definition error rather than a race.
//
// Value is whatever single JSON value the API returned: scalar, object or
// array. It is never filled in from PostgreSQL — a declared value silently
// standing in for an effective one is the failure this provider exists to
// avoid, which is why a failed read becomes a MissingContext instead.
type ConfigurationEntry struct {
	Path   string    `json:"path"`
	Key    string    `json:"key"`
	URL    string    `json:"url"`
	Value  any       `json:"value"`
	ReadAt time.Time `json:"read_at"`
}

// --- snapshot ------------------------------------------------------------

// ContextSnapshot is the immutable result of context building and the sole
// input the Rule Engine reasons over.
//
// The collections are flat rather than nested: VNFCs point at their owner
// through VNFC.VDUPath. A nested shape would force every rule to walk into a
// VDU to reach a VNFC, and most rules subject a VNFC directly.
//
// Every collection is sorted deterministically (VDUs/VNFCs by path, Links by
// src then dst, Configuration by path/key/url, MissingContext by provider
// order then entity/key) so two builds over the same data serialise to
// identical bytes. That is what makes the golden fixtures meaningful.
type ContextSnapshot struct {
	Input ContextInput `json:"input"`

	// Status is StatusComplete or StatusPartial. A snapshot only exists when
	// at least one profile matched; no match is an error, not an empty
	// snapshot.
	Status string `json:"status"`

	// Profiles are the names of the context profiles that matched, sorted.
	// Names, not IDs: context_profile.name is UNIQUE and is what an operator
	// reading a snapshot recognises.
	Profiles []string `json:"profiles,omitempty"`

	VDUs          []VDU                `json:"vdus"`
	VNFCs         []VNFC               `json:"vnfcs"`
	Links         []Link               `json:"links"`
	Configuration []ConfigurationEntry `json:"configuration"`

	// MissingContext names every target that was asked for and not obtained.
	// A gap is always recorded even though the snapshot stays usable: a
	// tolerated failure must still be nameable.
	MissingContext []MissingContext `json:"missing_context,omitempty"`

	// BuiltAt comes from an injected clock so fixtures are reproducible.
	BuiltAt time.Time `json:"built_at"`
}

// MissingContext is one target a provider was asked for and could not
// deliver.
//
// Entity is the thing addressed — a VDU path, a link rendered as
// "src_path->dst_path", or a configuration path. Key is set only where the
// target is finer than the entity, which today means the configuration key.
type MissingContext struct {
	Provider string `json:"provider"`
	Entity   string `json:"entity,omitempty"`
	Key      string `json:"key,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// --- rca status ----------------------------------------------------------

// RCA status values carried by RCAResult.Status, mapped to
// AnalysisStatus.rca on the wire.
//
// This is a separate vocabulary from the context status above even though two
// of the strings coincide. The context builder answers "did every provider
// answer"; RCA answers "did the rules reach a conclusion", and NO_CONCLUSION
// has no context-side counterpart. Aliasing the two would make a future
// divergence in either one silently change the other.
const (
	// RCAStatusComplete — at least one root cause, over a complete context,
	// with every rule row executing successfully. The only status that says
	// the answer is both conclusive and fully informed.
	RCAStatusComplete = "COMPLETE"

	// RCAStatusNoConclusion — everything ran correctly over a complete
	// context and no rule matched. A real answer, not a failure: the
	// operator's rule set has nothing to say about this incident.
	RCAStatusNoConclusion = "NO_CONCLUSION"

	// RCAStatusPartial — the context was partial, or at least one rule row
	// failed. Root causes may still be present; they were just reached with
	// less than the full picture, so they cannot be reported as COMPLETE.
	RCAStatusPartial = "PARTIAL"

	// RCAStatusFailed — no analysis happened at all: the repository errored,
	// no rule was enabled, or no row compiled and executed. Always returned
	// with an error.
	RCAStatusFailed = "FAILED"
)

// --- root cause vocabulary -----------------------------------------------

// Roles a root cause can hold, mirroring RootCause.role on the wire.
//
// The role is stated by the rule author, not derived: a rule that knows it is
// naming a contributing factor rather than the trigger says so, because
// nothing downstream can recover that distinction from a confidence number.
const (
	// RolePrimary — the rule believes this is the trigger.
	RolePrimary = "PRIMARY"

	// RoleContributing — a real factor that worsened the incident but did not
	// start it.
	RoleContributing = "CONTRIBUTING"

	// RoleSuspected — named on partial evidence, offered for an operator to
	// confirm.
	RoleSuspected = "SUSPECTED"
)

// Operations a recommended action can request, mirroring
// RecommendedAction.op. The vocabulary is RFC 6902-style deliberately: an
// action is a proposed change to a configuration document, and the consumer
// applying it should not have to interpret free-form verbs.
const (
	OpAdd     = "ADD"
	OpRemove  = "REMOVE"
	OpReplace = "REPLACE"
)

// --- rule execution vocabulary -------------------------------------------

// Per-row execution statuses recorded on RuleExecution.
//
// These describe what happened to one rca_rule row, which is a different
// question from what the analysis as a whole concluded — a run with one FAILED
// row and three COMPLETE rows is a PARTIAL analysis, not a failed one.
const (
	// RuleStatusComplete — the row compiled and executed to the end. It says
	// nothing about whether any rule inside it matched.
	RuleStatusComplete = "COMPLETE"

	// RuleStatusFailed — the row was reached and could not be completed:
	// compile error, evaluation error, cycle guard, invalid output, or a merge
	// conflict with an earlier row. All of the row's output is discarded.
	RuleStatusFailed = "FAILED"

	// RuleStatusSkipped — the request ran out of deadline before the row was
	// started. Recorded rather than omitted so a truncated run is visible as
	// truncated instead of looking like a rule set that had nothing to say.
	RuleStatusSkipped = "SKIPPED"
)

// --- rca domain ----------------------------------------------------------

// RuleDefinition is one enabled rca_rule row.
//
// Content is a whole scenario document and may hold several GRL rules with
// their own salience. The row is the unit the engine loads, compiles, caches
// and fails atomically; the rules inside it are the unit Grule fires. Keeping
// that distinction explicit is what lets one bad rule discard exactly its own
// document and nothing else.
type RuleDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Content  string `json:"content"`
	Salience int    `json:"salience"`

	UpdatedAt time.Time `json:"updated_at"`
}

// RCAResult is the sole output of the rule engine and the single source for
// the analysis status, the root causes and the internal execution trace.
//
// RuleExecutions is operational metadata for logs and tests. It is
// deliberately absent from the protobuf: a caller asking what went wrong with
// the engine's own rule set is asking an operator's question, and answering it
// on the response would make the rule inventory part of the public contract.
type RCAResult struct {
	Status     string      `json:"status"`
	RootCauses []RootCause `json:"root_causes"`

	RuleExecutions []RuleExecution `json:"rule_executions,omitempty"`
}

// RootCause is one conclusion a rule asserted, with the actions attached to
// it.
//
// ID is chosen by the rule author and is the join key between Assert and
// Recommend inside a document. Two rules asserting the same ID with the same
// metadata are the same finding reached twice; with different metadata they
// are a contradiction, which is a rule-set bug rather than something to
// average out.
type RootCause struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Summary  string `json:"summary"`

	// Entity is the LTREE path of the thing being blamed.
	Entity string `json:"entity"`

	Role string `json:"role"` // PRIMARY | CONTRIBUTING | SUSPECTED

	// Confidence is on a 0..1 scale and is stated by the rule, not computed.
	// There is no scoring policy left to interpret it: a rule that is less
	// sure says a smaller number.
	Confidence float64 `json:"confidence"`

	Actions []RecommendedAction `json:"actions,omitempty"`
}

// RecommendedAction is a change proposed to remedy a root cause.
//
// The engine only proposes; nothing here is executed. MOInstance addresses the
// managed object the change applies to, Code names the change, and Value is
// whatever JSON value the operation needs — a number for a replica count, a
// string for a lifecycle verb — which is why it is typed as any and maps to a
// protobuf Value on the wire.
type RecommendedAction struct {
	Code string `json:"code"`

	MOInstance string `json:"mo_instance"`
	Op         string `json:"op"` // ADD | REMOVE | REPLACE

	Value any `json:"value,omitempty"`
}

// RuleExecution is per-row operational metadata.
//
// Every loaded row produces exactly one record, including the rows that were
// never started. A row that ran and matched nothing is COMPLETE with a zero
// count — silence from a rule that ran and silence from a rule that never ran
// are different facts and must not collapse into the same absence.
type RuleExecution struct {
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name"`

	Status string `json:"status"` // COMPLETE | FAILED | SKIPPED

	// Error is the failure text for a FAILED row, empty otherwise.
	Error string `json:"error,omitempty"`

	// RootCauseCount is what this row contributed after merging, so a row
	// whose output was discarded reports 0.
	RootCauseCount int `json:"root_cause_count"`

	// Latency is wall-clock, so it is excluded from JSON: it would make every
	// golden fixture unstable and it means nothing outside the run that
	// produced it.
	Latency time.Duration `json:"-"`
}

// --- analysis result -----------------------------------------------------

// AnalysisResult is what one AnalyzeIncident call concluded, in the shape the
// response is built from.
//
// It carries strictly less than the stages produced. The snapshot, the rule
// execution trace and every internal latency stay behind: a caller asking what
// caused an incident is not asking which rule rows failed to compile, and
// publishing that would make the engine's own rule inventory part of the
// public contract. Building this type is where that line is drawn, so the
// transport never has to decide what is safe to send.
type AnalysisResult struct {
	// RequestID and Incident echo the validated request, so a caller
	// correlating a response never has to trust its own bookkeeping.
	RequestID string `json:"request_id"`
	Incident  string `json:"incident"`

	// OverallStatus is what the caller acts on. For a successful response it
	// equals RCAStatus: the rule engine already degrades itself to PARTIAL
	// when the context was incomplete, so a second opinion here could only
	// disagree with the one that had the evidence.
	OverallStatus string `json:"overall_status"`

	ContextStatus string `json:"context_status"`
	RCAStatus     string `json:"rca_status"`

	RootCauses []RootCause `json:"root_causes,omitempty"`

	// MissingContext comes only from the snapshot. A rule that failed is an
	// operator's problem with the rule set, not a gap in the context, and
	// reporting it here would send the caller looking at the wrong system.
	MissingContext []MissingContext `json:"missing_context,omitempty"`
}
