package analysis

import (
	"strings"
	"time"
)

const (
	StatusComplete = "COMPLETE"

	StatusPartial = "PARTIAL"
)

const (
	ProviderVDU           = "VDU"
	ProviderConfiguration = "CONFIGURATION"
	ProviderLink          = "LINK"
	ProviderMetric        = "METRIC"
)

const (
	// ReasonNotFound means the query succeeded, but the target data does not exist.
	ReasonNotFound = "NOT_FOUND"

	// ReasonQueryFailed means the database query failed, so it is unknown whether the target exists.
	ReasonQueryFailed = "QUERY_FAILED"

	// ReasonRequestFailed means the configuration request could not be completed,
	// for example due to a connection error, reset, or invalid URL.
	ReasonRequestFailed = "REQUEST_FAILED"

	// ReasonHTTPStatus means the configuration API responded with a non-2xx HTTP status.
	ReasonHTTPStatus = "HTTP_STATUS"

	// ReasonTimeout means the configuration request exceeded its allowed timeout.
	ReasonTimeout = "TIMEOUT"

	// ReasonEmptyBody means the API returned a successful 2xx response,
	// but the response body was empty.
	ReasonEmptyBody = "EMPTY_BODY"

	// ReasonInvalidJSON means the API returned a successful 2xx response,
	// but the response body was not valid JSON.
	ReasonInvalidJSON = "INVALID_JSON"
)

type ContextInput struct {
	RequestID string  `json:"request_id"`
	Incident  string  `json:"incident"`
	Alerts    []Alert `json:"alerts"`
}

type Alert struct {
	ID                    string         `json:"id"`
	SourcePath            string         `json:"source_path"`
	AlertType             string         `json:"alert_type,omitempty"`
	ProbableCause         string         `json:"probable_cause,omitempty"`
	PerceivedSeverity     string         `json:"perceived_severity,omitempty"`
	CreatedAt             string         `json:"created_at,omitempty"`
	AdditionalInformation map[string]any `json:"additional_information,omitempty"`
}

const AdditionalInformationRemoteIP = "remote_ip"

func (a Alert) RemoteIP() (string, bool) {
	value, ok := a.AdditionalInformation[AdditionalInformationRemoteIP].(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

type VDU struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Instances int    `json:"instances"`

	Selector string `json:"selector,omitempty"`

	ConfdPath   string `json:"confd_path,omitempty"`
	ConfdServer string `json:"confd_server,omitempty"`

	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type VNFC struct {
	Path    string `json:"path"`
	VDUPath string `json:"vdu_path"`

	K8sUID string `json:"k8s_uid,omitempty"`

	Name   string `json:"name"`
	Status string `json:"status"`

	Networks []map[string]any `json:"networks,omitempty"`

	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type ConfigurationEntry struct {
	Key    string    `json:"key"`
	Value  any       `json:"value"`
	ReadAt time.Time `json:"read_at"`
}

type LinkEntry struct {
	Target string    `json:"target"`
	Status string    `json:"status"`
	ReadAt time.Time `json:"read_at"`
}

type MetricEntry struct {
	Name   string    `json:"name"`
	Value  any       `json:"value"`
	ReadAt time.Time `json:"read_at"`
}

type ContextSnapshot struct {
	Input ContextInput `json:"input"`

	Status string `json:"status"`

	Profiles []string `json:"profiles,omitempty"`

	VDUs          []VDU                `json:"vdus"`
	VNFCs         []VNFC               `json:"vnfcs"`
	Configuration []ConfigurationEntry `json:"configuration"`
	Links         []LinkEntry          `json:"links"`
	Metrics       []MetricEntry        `json:"metrics"`

	MissingContext []MissingContext `json:"missing_context,omitempty"`

	BuiltAt time.Time `json:"built_at"`
}

type MissingContext struct {
	Provider string `json:"provider"`
	Entity   string `json:"entity,omitempty"`
	Key      string `json:"key,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

const (
	RCAStatusComplete = "COMPLETE"

	RCAStatusNoConclusion = "NO_CONCLUSION"

	RCAStatusPartial = "PARTIAL"

	RCAStatusFailed = "FAILED"
)

const (
	RolePrimary = "PRIMARY"

	RoleContributing = "CONTRIBUTING"

	RoleSuspected = "SUSPECTED"
)

const (
	OpAdd     = "ADD"
	OpRemove  = "REMOVE"
	OpReplace = "REPLACE"
	OpNotify  = "NOTIFY"
)

const (
	RuleStatusComplete = "COMPLETE"

	RuleStatusFailed = "FAILED"

	RuleStatusSkipped = "SKIPPED"
)

type RuleDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Content  string `json:"content"`
	Salience int    `json:"salience"`

	UpdatedAt time.Time `json:"updated_at"`
}

type RCAResult struct {
	Status     string      `json:"status"`
	RootCauses []RootCause `json:"root_causes"`

	RuleExecutions []RuleExecution `json:"rule_executions,omitempty"`
}

type RootCause struct {
	Category string `json:"category"`
	Role     string `json:"role"`
	Summary  string `json:"summary"`

	Components []Component `json:"components"`
}

type Component struct {
	Entity string             `json:"entity"`
	Action *RecommendedAction `json:"action,omitempty"`
}

type RecommendedAction struct {
	Code string `json:"code"`

	MOInstance string `json:"mo_instance"`
	Op         string `json:"op"`

	Value any `json:"value,omitempty"`
}

type RuleExecution struct {
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name"`

	Status string `json:"status"`

	Error string `json:"error,omitempty"`

	RootCauseCount int `json:"root_cause_count"`

	Passes int `json:"passes,omitempty"`

	Latency time.Duration `json:"-"`
}

type AnalysisResult struct {
	RequestID string `json:"request_id"`
	Incident  string `json:"incident"`

	OverallStatus string `json:"overall_status"`

	ContextStatus string `json:"context_status"`
	RCAStatus     string `json:"rca_status"`

	RootCauses []RootCause `json:"root_causes,omitempty"`

	MissingContext []MissingContext `json:"missing_context,omitempty"`
}
