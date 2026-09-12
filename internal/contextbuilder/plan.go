package contextbuilder

import (
	"sort"

	"re/internal/analysis"
)

type Plan struct {
	Profiles []string

	VDUs          []string
	Configuration []string
	Links         []LinkTarget
	Metrics       []string
}

func (p Plan) HasWork() bool {
	return len(p.VDUs) > 0 || len(p.Configuration) > 0 || len(p.Links) > 0 || len(p.Metrics) > 0
}

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
		configs = make(map[string]bool)
		links   = make(map[linkKey]linkSource)
		metrics = make(map[string]bool)
	)

	for _, prof := range matched {

		if err := prof.Validate(); err != nil {
			return Plan{}, err
		}

		p.Profiles = append(p.Profiles, prof.Name)

		for _, path := range prof.Providers.VDU {
			vdus[path] = true
		}
		for _, key := range prof.Providers.Configuration {
			configs[key] = true
		}
		for _, pr := range prof.Providers.Link {
			if err := mergeLink(links, pr, prof.Name); err != nil {
				return Plan{}, err
			}
		}
		for _, name := range prof.Providers.Metric {
			metrics[name] = true
		}
	}

	sort.Strings(p.Profiles)

	p.VDUs = make([]string, 0, len(vdus))
	for path := range vdus {
		p.VDUs = append(p.VDUs, path)
	}
	sort.Strings(p.VDUs)

	p.Configuration = make([]string, 0, len(configs))
	for key := range configs {
		p.Configuration = append(p.Configuration, key)
	}
	sort.Strings(p.Configuration)

	p.Links = make([]LinkTarget, 0, len(links))
	for _, src := range links {
		p.Links = append(p.Links, src.target)
	}
	sort.Slice(p.Links, func(i, j int) bool {
		return p.Links[i].Target < p.Links[j].Target
	})

	p.Metrics = make([]string, 0, len(metrics))
	for name := range metrics {
		p.Metrics = append(p.Metrics, name)
	}
	sort.Strings(p.Metrics)

	return p, nil
}

type linkKey struct {
	target string
}

type linkSource struct {
	target  LinkTarget
	profile string
}

func mergeLink(dst map[linkKey]linkSource, p LinkTarget, profile string) error {
	k := linkKey{target: p.Target}
	if _, ok := dst[k]; !ok {
		dst[k] = linkSource{target: p, profile: profile}
	}
	return nil
}
