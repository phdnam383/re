package contextbuilder

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"re/internal/analysis"
)

// Options are the Builder's collaborators. Every port is required except the
// clock and the logger.
//
// The providers are not optional the way IAE's are. There, a provider could be
// absent because its backend was unconfigured; here each provider's targets
// come from a profile and its backend is either PostgreSQL or a URL the
// profile itself supplies, so a nil provider is a wiring mistake rather than a
// deployment choice. Failing in New says so at startup instead of degrading
// every request to PARTIAL.
type Options struct {
	Profiles      ProfileRepository
	VDU           VDUProvider
	Link          LinkProvider
	Configuration ConfigurationProvider

	// Clock defaults to SystemClock.
	Clock Clock

	// Logger receives provider failures. It is the only place the underlying
	// error text survives: MissingContext carries a closed reason vocabulary
	// so a caller can act on it, which means the transport error behind a
	// QUERY_FAILED would otherwise be dropped entirely. Defaults to discarding.
	Logger *slog.Logger
}

// Builder turns a request into a ContextSnapshot.
type Builder struct {
	profiles      ProfileRepository
	vdu           VDUProvider
	link          LinkProvider
	configuration ConfigurationProvider
	clock         Clock
	logger        *slog.Logger
}

func New(opts Options) (*Builder, error) {
	switch {
	case opts.Profiles == nil:
		return nil, errors.New("contextbuilder: profile repository is required")
	case opts.VDU == nil:
		return nil, errors.New("contextbuilder: vdu provider is required")
	case opts.Link == nil:
		return nil, errors.New("contextbuilder: link provider is required")
	case opts.Configuration == nil:
		return nil, errors.New("contextbuilder: configuration provider is required")
	}

	b := &Builder{
		profiles:      opts.Profiles,
		vdu:           opts.VDU,
		link:          opts.Link,
		configuration: opts.Configuration,
		clock:         opts.Clock,
		logger:        opts.Logger,
	}
	if b.clock == nil {
		b.clock = SystemClock()
	}
	if b.logger == nil {
		b.logger = slog.New(slog.DiscardHandler)
	}
	return b, nil
}

// Build loads the enabled profiles, matches them against the request's alerts,
// merges every match into one plan, runs the providers over it and assembles
// the snapshot.
//
// Two kinds of failure are deliberately different:
//
//   - Returning an error means there is no snapshot. That covers a definition
//     the engine cannot use, no matching profile, and the caller giving up
//     (cancellation or deadline). Nothing partial is worth handing to the rule
//     engine in those cases.
//   - A provider coming back short returns a snapshot with StatusPartial. The
//     other providers' results are kept and every affected target is named in
//     MissingContext, because a tolerated gap must still be nameable.
func (b *Builder) Build(ctx context.Context, in analysis.ContextInput) (analysis.ContextSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return analysis.ContextSnapshot{}, err
	}

	profiles, err := b.profiles.LoadEnabled(ctx)
	if err != nil {
		return analysis.ContextSnapshot{}, err
	}

	// resolve validates every matched profile, so nothing reaches a provider
	// that has not been checked. A no-match returns ErrContextProfileNotFound
	// here and the providers are never touched.
	plan, err := resolve(in.Alerts, profiles)
	if err != nil {
		return analysis.ContextSnapshot{}, err
	}

	results := b.runProviders(ctx, plan)

	// Cancellation outranks a degraded snapshot. Every provider will have
	// failed once the context is done, and reporting that as PARTIAL would
	// describe a caller who walked away as an infrastructure problem.
	if err := ctx.Err(); err != nil {
		return analysis.ContextSnapshot{}, err
	}

	return b.assemble(in, plan, results), nil
}

// providerResults is what the fan-out produces. Each goroutine writes exactly
// one field, so the results need no mutex and the merge below is ordered by
// the code rather than by which provider finished first.
type providerResults struct {
	vdu           VDUResult
	link          LinkResult
	configuration ConfigurationResult
}

// runProviders runs the three providers concurrently, skipping any with no
// targets.
//
// Whether a provider runs is derived from whether there is work for it, never
// from a flag on a profile: a profile that named no links has not asked for a
// link lookup, and issuing one anyway would report gaps nobody enquired about.
func (b *Builder) runProviders(ctx context.Context, plan Plan) providerResults {
	var (
		out providerResults
		wg  sync.WaitGroup
	)

	if len(plan.VDUs) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.vdu.FetchVDUs(ctx, plan.VDUs)
			if err != nil {
				b.logger.WarnContext(ctx, "vdu provider failed",
					slog.String("provider", analysis.ProviderVDU),
					slog.Int("targets", len(plan.VDUs)),
					slog.Any("error", err))
				out.vdu = VDUResult{Missing: missingForVDUs(plan.VDUs, analysis.ReasonQueryFailed)}
				return
			}
			out.vdu = res
		}()
	}

	if len(plan.Links) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.link.FetchLinks(ctx, plan.Links)
			if err != nil {
				b.logger.WarnContext(ctx, "link provider failed",
					slog.String("provider", analysis.ProviderLink),
					slog.Int("targets", len(plan.Links)),
					slog.Any("error", err))
				out.link = LinkResult{Missing: missingForLinks(plan.Links, analysis.ReasonQueryFailed)}
				return
			}
			out.link = res
		}()
	}

	if len(plan.Configuration) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.configuration.FetchConfiguration(ctx, plan.Configuration)
			if err != nil {
				b.logger.WarnContext(ctx, "configuration provider failed",
					slog.String("provider", analysis.ProviderConfiguration),
					slog.Int("targets", len(plan.Configuration)),
					slog.Any("error", err))
				out.configuration = ConfigurationResult{
					Missing: missingForConfiguration(plan.Configuration, analysis.ReasonRequestFailed),
				}
				return
			}
			out.configuration = res
		}()
	}

	wg.Wait()
	return out
}

// A provider that fails as a whole tells us nothing about any individual
// target, so every target it was given becomes a gap. Anything it may have
// returned alongside the error is discarded: half a result that the provider
// itself considered failed is not something a rule should reason over.

func missingForVDUs(paths []string, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(paths))
	for _, path := range paths {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderVDU, Entity: path, Reason: reason,
		})
	}
	return out
}

func missingForLinks(targets []LinkTarget, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(targets))
	for _, t := range targets {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderLink, Entity: t.Entity(), Reason: reason,
		})
	}
	return out
}

func missingForConfiguration(targets []ConfigurationTarget, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(targets))
	for _, t := range targets {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderConfiguration, Entity: t.Path, Key: t.Key, Reason: reason,
		})
	}
	return out
}
