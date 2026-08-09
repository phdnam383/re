package contextbuilder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"re/internal/analysis"
)

// --- errors --------------------------------------------------------------

// ErrContextProfileNotFound is returned when no enabled profile's selector
// matches any alert in the request.
//
// This is an error and not an empty snapshot on purpose. A profile is the only
// thing that says what a request may fetch, so with none matched there is no
// declared scope — and a snapshot carrying empty collections would reach the
// rule engine looking exactly like a topology that was fetched and found to be
// empty. No provider is called in this case.
var ErrContextProfileNotFound = errors.New("missing context_profile")

// DefinitionError reports a stored context_profile that cannot be used as
// written: an unknown provider key, a malformed ltree path, a configuration
// URL that is not http(s), a selector that would match everything.
//
// It is deliberately fatal to the build rather than a skipped profile. These
// are authoring mistakes, and an engine that quietly ignored one would analyse
// a narrower scope than the operator declared while reporting success. Callers
// discriminate with errors.As.
type DefinitionError struct {
	Profile string // context_profile.name; empty if not yet known
	Field   string // dotted path within the row, e.g. "providers.configuration[0].url"
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

// --- repository ----------------------------------------------------------

// ProfileRepository loads the context profiles the builder matches against.
//
// The Context Builder loads its own profiles rather than receiving them: the
// rule engine loads rules the same way, and neither should be able to run
// against definitions the other resolved.
type ProfileRepository interface {
	// LoadEnabled returns every profile with enabled = TRUE, ordered by name,
	// decoded and validated. A query or decode failure is a build failure —
	// see DefinitionError.
	LoadEnabled(ctx context.Context) ([]ContextProfile, error)
}

// --- providers -----------------------------------------------------------

// Each provider takes the exact targets the merged plan asks for and reports,
// per target, either a result or a MissingContext. A returned error means the
// provider failed as a whole; the builder then records a MissingContext for
// every target that provider was given, because a provider knows what it
// failed to fetch but the builder is what owns the snapshot.

// VDUProvider fetches VDU rows by exact path together with every VNFC in each
// VDU's subtree.
type VDUProvider interface {
	FetchVDUs(ctx context.Context, paths []string) (VDUResult, error)
}

// VDUResult carries both collections because they come from one round trip and
// a VNFC is only meaningful next to the VDU it was found under.
//
// A VDU that exists with no VNFC at all is a normal result, not a gap: a
// workload scaled to zero is a fact a rule may want to conclude from.
type VDUResult struct {
	VDUs    []analysis.VDU
	VNFCs   []analysis.VNFC
	Missing []analysis.MissingContext
}

// LinkProvider fetches link rows for exact directed pairs. It performs no
// traversal and never substitutes the reverse pair — a profile that wants both
// directions names both.
type LinkProvider interface {
	FetchLinks(ctx context.Context, targets []LinkTarget) (LinkResult, error)
}

type LinkResult struct {
	Links   []analysis.Link
	Missing []analysis.MissingContext
}

// ConfigurationProvider reads effective configuration from an external API,
// one independent GET per target.
type ConfigurationProvider interface {
	FetchConfiguration(ctx context.Context, targets []ConfigurationTarget) (ConfigurationResult, error)
}

type ConfigurationResult struct {
	Entries []analysis.ConfigurationEntry
	Missing []analysis.MissingContext
}

// --- clock ---------------------------------------------------------------

// Clock supplies the timestamps stamped on a snapshot (ContextSnapshot.BuiltAt)
// and on each configuration read (ConfigurationEntry.ReadAt). It is injected so
// the golden fixtures can pin a build to a fixed instant; production passes
// SystemClock.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// SystemClock reads the wall clock.
func SystemClock() Clock { return ClockFunc(time.Now) }
