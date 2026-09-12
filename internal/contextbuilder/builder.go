package contextbuilder

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"re/internal/analysis"
)

type Options struct {
	Profiles      ProfileRepository
	VDU           VDUProvider
	Configuration ConfigurationProvider
	Link          LinkProvider
	Metric        MetricProvider

	Clock Clock

	Logger *slog.Logger
}

type Builder struct {
	profiles      ProfileRepository
	vdu           VDUProvider
	configuration ConfigurationProvider
	link          LinkProvider
	metric        MetricProvider
	clock         Clock
	logger        *slog.Logger
}

func New(opts Options) (*Builder, error) {
	switch {
	case opts.Profiles == nil:
		return nil, errors.New("contextbuilder: profile repository is required")
	case opts.VDU == nil:
		return nil, errors.New("contextbuilder: vdu provider is required")
	case opts.Configuration == nil:
		return nil, errors.New("contextbuilder: configuration provider is required")
	case opts.Link == nil:
		return nil, errors.New("contextbuilder: link provider is required")
	case opts.Metric == nil:
		return nil, errors.New("contextbuilder: metric provider is required")
	}

	b := &Builder{
		profiles:      opts.Profiles,
		vdu:           opts.VDU,
		configuration: opts.Configuration,
		link:          opts.Link,
		metric:        opts.Metric,
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

func (b *Builder) Build(ctx context.Context, in analysis.ContextInput) (analysis.ContextSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return analysis.ContextSnapshot{}, err
	}

	profiles, err := b.profiles.LoadEnabled(ctx)
	if err != nil {
		return analysis.ContextSnapshot{}, err
	}

	plan, err := resolve(in.Alerts, profiles)
	if err != nil {
		return analysis.ContextSnapshot{}, err
	}
	plan = applyAutomaticPlan(in, plan)

	results := b.runProviders(ctx, alertSourcePath(in), plan)

	if err := ctx.Err(); err != nil {
		return analysis.ContextSnapshot{}, err
	}

	snap := b.assemble(in, plan, results)
	b.logger.InfoContext(ctx, "context snapshot built",
		slog.String("request_id", in.RequestID),
		slog.String("status", snap.Status),
		slog.Any("snapshot", snap))
	return snap, nil
}

type providerResults struct {
	vdu           VDUResult
	configuration ConfigurationResult
	link          LinkResult
	metric        MetricResult
}

func (b *Builder) runProviders(ctx context.Context, vnfcPath string, plan Plan) providerResults {
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

	if len(plan.Configuration) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.configuration.FetchConfiguration(ctx, vnfcPath, plan.Configuration)
			if err != nil {
				b.logger.WarnContext(ctx, "configuration provider failed",
					slog.String("provider", analysis.ProviderConfiguration),
					slog.Int("targets", len(plan.Configuration)),
					slog.Any("error", err))
				out.configuration = ConfigurationResult{
					Missing: missingForConfiguration(plan.Configuration, vnfcPath, analysis.ReasonRequestFailed),
				}
				return
			}
			out.configuration = res
		}()
	}

	if len(plan.Links) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.link.FetchLinks(ctx, vnfcPath, plan.Links)
			if err != nil {
				b.logger.WarnContext(ctx, "link provider failed",
					slog.String("provider", analysis.ProviderLink),
					slog.Int("targets", len(plan.Links)),
					slog.Any("error", err))
				out.link = LinkResult{
					Missing: missingForLinks(plan.Links, analysis.ReasonRequestFailed),
				}
				return
			}
			out.link = res
		}()
	}

	if len(plan.Metrics) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := b.metric.FetchMetrics(ctx, vnfcPath, plan.Metrics)
			if err != nil {
				b.logger.WarnContext(ctx, "metric provider failed",
					slog.String("provider", analysis.ProviderMetric),
					slog.Int("targets", len(plan.Metrics)),
					slog.Any("error", err))
				out.metric = MetricResult{
					Missing: missingForMetrics(plan.Metrics, vnfcPath, analysis.ReasonRequestFailed),
				}
				return
			}
			out.metric = res
		}()
	}

	wg.Wait()
	return out
}

func missingForVDUs(paths []string, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(paths))
	for _, path := range paths {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderVDU, Entity: path, Reason: reason,
		})
	}
	return out
}

func missingForConfiguration(keys []string, sourcePath, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(keys))
	for _, key := range keys {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderConfiguration, Entity: sourcePath, Key: key, Reason: reason,
		})
	}
	return out
}

func alertSourcePath(in analysis.ContextInput) string {
	if len(in.Alerts) == 0 {
		return ""
	}
	return in.Alerts[0].SourcePath
}

func missingForLinks(targets []LinkTarget, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(targets))
	for _, t := range targets {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderLink, Entity: t.Target, Reason: reason,
		})
	}
	return out
}

func missingForMetrics(names []string, vnfcPath, reason string) []analysis.MissingContext {
	out := make([]analysis.MissingContext, 0, len(names))
	for _, name := range names {
		out = append(out, analysis.MissingContext{
			Provider: analysis.ProviderMetric, Entity: vnfcPath, Key: name, Reason: reason,
		})
	}
	return out
}
