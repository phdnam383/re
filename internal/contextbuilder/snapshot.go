package contextbuilder

import (
	"sort"

	"re/internal/analysis"
)

func (b *Builder) assemble(in analysis.ContextInput, plan Plan, res providerResults) analysis.ContextSnapshot {
	snap := analysis.ContextSnapshot{
		Input:    in,
		Profiles: plan.Profiles,

		VDUs:          orEmpty(res.vdu.VDUs),
		VNFCs:         orEmpty(res.vdu.VNFCs),
		Configuration: orEmpty(res.configuration.Entries),
		Links:         orEmpty(res.link.Entries),
		Metrics:       orEmpty(res.metric.Entries),

		BuiltAt: b.clock.Now().UTC(),
	}

	snap.MissingContext = append(snap.MissingContext, res.vdu.Missing...)
	snap.MissingContext = append(snap.MissingContext, res.configuration.Missing...)
	snap.MissingContext = append(snap.MissingContext, res.link.Missing...)
	snap.MissingContext = append(snap.MissingContext, res.metric.Missing...)

	sortSnapshot(&snap)

	snap.Status = analysis.StatusComplete
	if len(snap.MissingContext) > 0 {
		snap.Status = analysis.StatusPartial
	}
	return snap
}

func sortSnapshot(snap *analysis.ContextSnapshot) {
	sort.Strings(snap.Profiles)

	sort.Slice(snap.VDUs, func(i, j int) bool {
		return snap.VDUs[i].Path < snap.VDUs[j].Path
	})
	sort.Slice(snap.VNFCs, func(i, j int) bool {
		return snap.VNFCs[i].Path < snap.VNFCs[j].Path
	})

	sort.Slice(snap.Configuration, func(i, j int) bool {
		return snap.Configuration[i].Key < snap.Configuration[j].Key
	})

	sort.Slice(snap.Links, func(i, j int) bool {
		a, b := snap.Links[i], snap.Links[j]
		return a.Target < b.Target
	})

	sort.Slice(snap.Metrics, func(i, j int) bool {
		return snap.Metrics[i].Name < snap.Metrics[j].Name
	})

	sort.Slice(snap.MissingContext, func(i, j int) bool {
		a, b := snap.MissingContext[i], snap.MissingContext[j]
		if ra, rb := providerRank(a.Provider), providerRank(b.Provider); ra != rb {
			return ra < rb
		}
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		return a.Key < b.Key
	})
}

func providerRank(provider string) int {
	switch provider {
	case analysis.ProviderVDU:
		return 0
	case analysis.ProviderMetric:
		return 1
	case analysis.ProviderConfiguration:
		return 2
	case analysis.ProviderLink:
		return 3
	default:
		return 4
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
