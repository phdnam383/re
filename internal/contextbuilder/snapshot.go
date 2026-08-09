package contextbuilder

import (
	"sort"

	"re/internal/analysis"
)

// assemble merges the provider results into the snapshot.
//
// The merge order is fixed at VDU → LINK → CONFIGURATION and every collection
// is then sorted, so two builds over the same data with the same clock
// serialise to identical bytes. That is a property of the contract rather than
// of the implementation: the snapshot is what the golden fixtures pin, and a
// collection whose order depended on which provider answered first would make
// every one of them flaky.
func (b *Builder) assemble(in analysis.ContextInput, plan Plan, res providerResults) analysis.ContextSnapshot {
	snap := analysis.ContextSnapshot{
		Input:    in,
		Profiles: plan.Profiles,

		// Non-nil even when empty: an absent collection and an empty one are
		// the same fact here, and `[]` keeps the serialised shape stable
		// whether or not a profile asked for that provider.
		VDUs:          orEmpty(res.vdu.VDUs),
		VNFCs:         orEmpty(res.vdu.VNFCs),
		Links:         orEmpty(res.link.Links),
		Configuration: orEmpty(res.configuration.Entries),

		BuiltAt: b.clock.Now().UTC(),
	}

	snap.MissingContext = append(snap.MissingContext, res.vdu.Missing...)
	snap.MissingContext = append(snap.MissingContext, res.link.Missing...)
	snap.MissingContext = append(snap.MissingContext, res.configuration.Missing...)

	sortSnapshot(&snap)

	// Any gap at all is PARTIAL. There is no notion of a tolerated provider:
	// a profile named every target explicitly, so nothing in the plan is
	// incidental and a caller is better placed than the builder to decide
	// whether a particular gap matters to it.
	snap.Status = analysis.StatusComplete
	if len(snap.MissingContext) > 0 {
		snap.Status = analysis.StatusPartial
	}
	return snap
}

func sortSnapshot(snap *analysis.ContextSnapshot) {
	sort.Strings(snap.Profiles)

	// Paths are primary keys, so path order is a total order for both.
	sort.Slice(snap.VDUs, func(i, j int) bool {
		return snap.VDUs[i].Path < snap.VDUs[j].Path
	})
	sort.Slice(snap.VNFCs, func(i, j int) bool {
		return snap.VNFCs[i].Path < snap.VNFCs[j].Path
	})

	sort.Slice(snap.Links, func(i, j int) bool {
		a, b := snap.Links[i], snap.Links[j]
		if a.SrcPath != b.SrcPath {
			return a.SrcPath < b.SrcPath
		}
		return a.DstPath < b.DstPath
	})

	sort.Slice(snap.Configuration, func(i, j int) bool {
		a, b := snap.Configuration[i], snap.Configuration[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.URL < b.URL
	})

	// Provider first, so the missing list reads in the same VDU → LINK →
	// CONFIGURATION order the collections above do; then entity and key, which
	// are unique within a provider because the plan deduplicated the targets.
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

// providerRank fixes the provider ordering. Unknown providers sort last rather
// than panicking: this ordering is presentation, and a future provider that
// forgot to register here should look out of place, not take a request down.
func providerRank(provider string) int {
	switch provider {
	case analysis.ProviderVDU:
		return 0
	case analysis.ProviderLink:
		return 1
	case analysis.ProviderConfiguration:
		return 2
	default:
		return 3
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
