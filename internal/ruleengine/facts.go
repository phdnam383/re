package ruleengine

import (
	"math"

	"re/internal/analysis"
)

const (
	statusRunning    = "RUNNING"
	statusTerminated = "TERMINATED"
)

const (
	causeThresholdCrossing = "THRESHOLD_CROSSING"
	overloadMetricPrefix   = "overload"
)

const (
	cfgKeyLogFileCount = "log_file_count"
	cfgKeyLogFileSize  = "log_file_size"
	cfgKeyLogLevel     = "log_level"
	cfgKeyMemoryLimit  = "limit_memory"
)

const (
	metricErlProcess = "process"
	metricErlPort    = "port"
	metricErlAtom    = "atom"
)

const (
	linkStatusSuccess = "OK"
	linkStatusFailed  = "UNREACHABLE"
)
const (
	logLevelDebug = "DEBUG"
	logLevelTrace = "TRACE"
)

type Facts struct {
	Alert  *AlertFacts
	Vdu    *VDUFacts
	Vnfc   *VNFCFacts
	Cfg    *ConfigurationFacts
	Erl    *ErlFacts
	Table  *TableFacts
	Link   *LinkFacts
	Metric *MetricFacts

	v *view
}

func NewFacts(snap analysis.ContextSnapshot) *Facts {
	v := newView(snap)
	return &Facts{
		Alert:  &AlertFacts{v: v},
		Vdu:    &VDUFacts{v: v},
		Vnfc:   &VNFCFacts{v: v},
		Cfg:    &ConfigurationFacts{v: v},
		Erl:    &ErlFacts{v: v},
		Table:  &TableFacts{v: v},
		Link:   &LinkFacts{v: v},
		Metric: &MetricFacts{v: v},
		v:      v,
	}
}

// ---------------------------------Alert--------------------------------------------------
type AlertFacts struct{ v *view }

func (f *AlertFacts) HasCause(cause string) bool {
	for _, alert := range f.v.snap.Input.Alerts {
		if equalFold(alert.ProbableCause, cause) {
			return true
		}
	}
	return false
}

func (f *AlertFacts) SourcePath() string { return f.alert().SourcePath }

func (f *AlertFacts) VduPath() string {
	source := f.alert().SourcePath
	if source == "" {
		return ""
	}
	if d, ok := f.v.vduByPath[source]; ok {
		return d.Path
	}
	if n, ok := f.v.vnfcByPath[source]; ok {
		return n.VDUPath
	}
	return ""
}

func (f *AlertFacts) alert() analysis.Alert {
	if len(f.v.snap.Input.Alerts) == 0 {
		return analysis.Alert{}
	}
	return f.v.snap.Input.Alerts[0]
}

// ---------------------------------Vdu----------------------------------------------------
type VDUFacts struct{ v *view }

func (f *VDUFacts) DesiredReplicas(path string) int { return f.v.vduByPath[path].Instances }

func (f *VDUFacts) ReadyReplicas(path string) int { return f.v.readyReplicas(path) }

func (f *VDUFacts) IsDegraded(path string) bool {
	desired := f.DesiredReplicas(path)
	return desired > 0 && f.ReadyReplicas(path) < desired
}

// ---------------------------------Vnfc---------------------------------------------------
type VNFCFacts struct{ v *view }

func (f *VNFCFacts) Status(path string) string { return f.v.vnfcByPath[path].Status }

func (f *VNFCFacts) IsDown(path string) bool {
	return equalFold(f.Status(path), statusTerminated)
}

func (f *VNFCFacts) HasAnyDownInVDU(vduPath string) bool {
	return len(f.DownPathsInVDU(vduPath)) > 0
}

func (f *VNFCFacts) DownPathsInVDU(vduPath string) []string {
	var out []string
	for _, vnfc := range f.v.vnfcsByVDU[vduPath] {
		if equalFold(vnfc.Status, statusTerminated) {
			out = append(out, vnfc.Path)
		}
	}
	return out
}

func (f *VNFCFacts) Parent(path string) string { return f.v.vnfcByPath[path].VDUPath }

// ---------------------------------Configuration-------------------------------------------
type ConfigurationFacts struct{ v *view }

func (f *ConfigurationFacts) entry(path, key string) (analysis.ConfigurationEntry, bool) {
	if len(f.v.snap.Input.Alerts) == 0 || path != f.v.snap.Input.Alerts[0].SourcePath {
		return analysis.ConfigurationEntry{}, false
	}
	e, ok := f.v.configByKey[key]
	return e, ok
}

func (f *ConfigurationFacts) Has(path, key string) bool {
	_, ok := f.entry(path, key)
	return ok
}

func (f *ConfigurationFacts) GetFloat(path, key string) float64 {
	e, ok := f.entry(path, key)
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

func (f *ConfigurationFacts) GetString(path, key string) string {
	e, ok := f.entry(path, key)
	if !ok {
		return ""
	}
	s, _ := e.Value.(string)
	return s
}

func (f *ConfigurationFacts) LogFileCount(path string) float64 {
	return f.GetFloat(path, cfgKeyLogFileCount)
}

func (f *ConfigurationFacts) LogFileSizeMB(path string) float64 {
	return f.GetFloat(path, cfgKeyLogFileSize)
}

func (f *ConfigurationFacts) MemoryLimitMB(path string) float64 {
	return f.GetFloat(path, cfgKeyMemoryLimit)
}

func (f *ConfigurationFacts) LogLevel(path string) string {
	return f.GetString(path, cfgKeyLogLevel)
}

func (f *ConfigurationFacts) IsLogVerbose(path string) bool {
	level := f.GetString(path, cfgKeyLogLevel)
	return equalFold(level, logLevelDebug) || equalFold(level, logLevelTrace)
}

func (f *ConfigurationFacts) LogFootprintRatio(path string) float64 {
	limit := f.MemoryLimitMB(path)
	if limit <= 0 {
		return 0
	}
	return f.LogFileCount(path) * f.LogFileSizeMB(path) / limit
}

// ---------------------------------Erl----------------------------------------------------
type ErlFacts struct{ v *view }

func (f *ErlFacts) ProcessRatio(path string) float64 { return metricRatio(f.v, path, metricErlProcess) }

func (f *ErlFacts) PortRatio(path string) float64 { return metricRatio(f.v, path, metricErlPort) }

func (f *ErlFacts) AtomRatio(path string) float64 { return metricRatio(f.v, path, metricErlAtom) }

// ---------------------------------Table--------------------------------------------------
type TableFacts struct{ v *view }

func (f *TableFacts) FillRatio(path, table string) float64 {
	return metricRatio(f.v, path, table)
}

func (f *TableFacts) RowsAbove(path, table string, ratio float64) int {
	pair, ok := f.v.metric(path, table)
	if !ok || pair[1] <= 0 {
		return 0
	}
	above := pair[0] - ratio*pair[1]
	if above <= 0 {
		return 0
	}
	return int(above)
}

func metricRatio(v *view, path, metric string) float64 {
	pair, ok := v.metric(path, metric)
	if !ok || pair[1] <= 0 {
		return 0
	}
	return pair[0] / pair[1]
}

// ---------------------------------Link--------------------------------------------------
type LinkFacts struct{ v *view }

// PingToRemoteFailed evaluates the automatically collected probe whose target
// comes from the primary alert's additional_information.remote_ip.
func (f *LinkFacts) PingToRemoteFailed() bool {
	target, ok := f.remoteIP()
	return ok && linkStatus(f.v, target, linkStatusFailed)
}

// PingToRemoteSuccess evaluates the automatically collected probe whose target
// comes from the primary alert's additional_information.remote_ip.
func (f *LinkFacts) PingToRemoteSuccess() bool {
	target, ok := f.remoteIP()
	return ok && linkStatus(f.v, target, linkStatusSuccess)
}

func (f *LinkFacts) PingFails(path string) bool {
	return linkStatus(f.v, path, linkStatusFailed)
}

func (f *LinkFacts) PingAnswers(path string) bool {
	return linkStatus(f.v, path, linkStatusSuccess)
}

func (f *LinkFacts) remoteIP() (string, bool) {
	if len(f.v.snap.Input.Alerts) == 0 {
		return "", false
	}
	return f.v.snap.Input.Alerts[0].RemoteIP()
}

func linkStatus(v *view, path, want string) bool {
	entry, ok := v.link(path)
	if !ok {
		return false
	}
	return equalFold(entry.Status, want)
}

// ---------------------------------Metric------------------------------------------------
type MetricFacts struct{ v *view }

func (f *MetricFacts) Value(name string) float64 {
	entry, ok := f.v.metricEntry(name)
	if !ok {
		return 0
	}
	value, ok := asFloat(entry.Value)
	if !ok {
		return 0
	}
	return value
}

func (f *MetricFacts) AbsValue(name string) float64 {
	return math.Abs(f.Value(name))
}
