package contextbuilder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"re/internal/analysis"
)

var ErrContextProfileNotFound = errors.New("missing context_profile")

type DefinitionError struct {
	Profile string
	Field   string
	Detail  string
}

func (e *DefinitionError) Error() string {
	if e.Profile == "" {
		return fmt.Sprintf("context_profile: %s: %s", e.Field, e.Detail)
	}
	return fmt.Sprintf("context_profile %q: %s: %s", e.Profile, e.Field, e.Detail)
}

func definitionErrorf(profile, field, format string, args ...any) error {
	return &DefinitionError{Profile: profile, Field: field, Detail: fmt.Sprintf(format, args...)}
}

type ProfileRepository interface {
	LoadEnabled(ctx context.Context) ([]ContextProfile, error)
}

type VDUProvider interface {
	FetchVDUs(ctx context.Context, paths []string) (VDUResult, error)
}

type VDUResult struct {
	VDUs    []analysis.VDU
	VNFCs   []analysis.VNFC
	Missing []analysis.MissingContext
}

type ConfigurationProvider interface {
	FetchConfiguration(ctx context.Context, sourcePath string, keys []string) (ConfigurationResult, error)
}

type ConfigurationResult struct {
	Entries []analysis.ConfigurationEntry
	Missing []analysis.MissingContext
}

type LinkProvider interface {
	FetchLinks(ctx context.Context, vnfcPath string, targets []LinkTarget) (LinkResult, error)
}

type LinkResult struct {
	Entries []analysis.LinkEntry
	Missing []analysis.MissingContext
}

type MetricProvider interface {
	FetchMetrics(ctx context.Context, vnfcPath string, names []string) (MetricResult, error)
}

type MetricResult struct {
	Entries []analysis.MetricEntry
	Missing []analysis.MissingContext
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

func SystemClock() Clock { return ClockFunc(time.Now) }
