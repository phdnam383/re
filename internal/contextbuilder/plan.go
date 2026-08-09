package contextbuilder

import (
	"sort"

	"re/internal/analysis"
)

// Plan is the merged, deduplicated work every matched profile asked for. It
// holds concrete targets only: by the time a Plan exists there is nothing left
// to interpret, and each provider receives exactly the slice named here.
//
// Every collection is sorted, which is what makes a build reproducible: the
// providers preserve the order they are given, so the plan's order reaches the
// snapshot and the golden fixtures.
type Plan struct {
	// Profiles are the names of the profiles that matched, sorted. They are
	// reported in the snapshot so a reader can tell which definitions produced
	// the scope.
	Profiles []string

	VDUs          []string
	Links         []LinkTarget
	Configuration []ConfigurationTarget
}

// HasWork reports whether any provider has targets. A matched profile that
// asks for nothing yields an empty plan, and an empty plan still produces a
// COMPLETE snapshot — nothing was requested, so nothing is missing.
func (p Plan) HasWork() bool {
	return len(p.VDUs) > 0 || len(p.Links) > 0 || len(p.Configuration) > 0
}

// resolve matches the alerts against the profiles and merges every match into
// one plan.
//
// Profiles are merged, not ranked. A request carrying several kinds of alert
// legitimately needs the union of what each matched profile asks for, and
// every merge operation here is a set union, so no ordering of the profiles
// could change the result. They are still processed in name order so that a
// conflict is always reported against the same pair.
//
// No match is ErrContextProfileNotFound, not an empty plan: see the error's
// documentation. The caller must not call a provider after this returns an
// error.
func resolve(alerts []analysis.Alert, profiles []ContextProfile) (Plan, error) {
	matched := make([]ContextProfile, 0, len(profiles))
	for _, prof := range profiles {
		if prof.Selector.Matches(alerts) {
			matched = append(matched, prof)
		}
	}
	if len(matched) == 0 {
		return Plan{}, ErrContextProfileNotFound
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })

	var (
		p       Plan
		vdus    = make(map[string]bool)
		links   = make(map[LinkTarget]bool)
		configs = make(map[configKey]configSource)
	)

	for _, prof := range matched {
		// Re-validated here, not only at decode time, so "validated before any
		// provider is called" holds for a profile built as a struct literal in
		// a test as well as for one read from the database.
		if err := prof.Validate(); err != nil {
			return Plan{}, err
		}

		p.Profiles = append(p.Profiles, prof.Name)

		for _, path := range prof.Providers.VDU {
			vdus[path] = true
		}
		for _, l := range prof.Providers.Link {
			// Identity is the whole directed pair, so two profiles naming the
			// same link agree by construction and there is nothing to conflict.
			links[l] = true
		}
		for _, c := range prof.Providers.Configuration {
			if err := mergeConfiguration(configs, c, prof.Name); err != nil {
				return Plan{}, err
			}
		}
	}

	sort.Strings(p.Profiles)

	p.VDUs = make([]string, 0, len(vdus))
	for path := range vdus {
		p.VDUs = append(p.VDUs, path)
	}
	sort.Strings(p.VDUs)

	p.Links = make([]LinkTarget, 0, len(links))
	for l := range links {
		p.Links = append(p.Links, l)
	}
	sort.Slice(p.Links, func(i, j int) bool {
		if p.Links[i].SrcPath != p.Links[j].SrcPath {
			return p.Links[i].SrcPath < p.Links[j].SrcPath
		}
		return p.Links[i].DstPath < p.Links[j].DstPath
	})

	p.Configuration = make([]ConfigurationTarget, 0, len(configs))
	for _, src := range configs {
		p.Configuration = append(p.Configuration, src.target)
	}
	sort.Slice(p.Configuration, func(i, j int) bool {
		a, b := p.Configuration[i], p.Configuration[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.URL < b.URL
	})

	return p, nil
}

// configKey is a configuration target's identity: what is being read, not
// where from. The URL is deliberately excluded — that is what makes two
// profiles disagreeing about it detectable instead of producing two entries.
type configKey struct {
	path string
	key  string
}

// configSource remembers which profile contributed a target so a conflict can
// name both sides. An error saying only that a key is ambiguous leaves the
// operator to grep every enabled profile for it.
type configSource struct {
	target  ConfigurationTarget
	profile string
}

// mergeConfiguration unions a target into the plan, rejecting a second URL for
// a (path, key) already claimed.
//
// This has to be an error rather than a choice. Both URLs were declared by an
// operator, the engine has no basis to prefer one, and picking either would
// put a value into the snapshot that half the definitions say is read from
// somewhere else. Fetching both is no better: ConfigurationEntry is keyed by
// (path, key), so a rule reading it would get whichever won a race.
func mergeConfiguration(dst map[configKey]configSource, c ConfigurationTarget, profile string) error {
	k := configKey{path: c.Path, key: c.Key}
	existing, ok := dst[k]
	if !ok {
		dst[k] = configSource{target: c, profile: profile}
		return nil
	}
	if existing.target.URL != c.URL {
		return definitionErrorf(profile, "providers.configuration",
			"%s/%s is declared with url %q here and %q in profile %q; a configuration key must resolve to one url",
			c.Path, c.Key, c.URL, existing.target.URL, existing.profile)
	}
	return nil
}
