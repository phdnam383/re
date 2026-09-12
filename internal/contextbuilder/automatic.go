package contextbuilder

import (
	"sort"

	"re/internal/analysis"
)

type automaticPlanEnricher func(analysis.ContextInput, *Plan)

var automaticPlanEnrichers = [...]automaticPlanEnricher{
	addRemoteIPLink,
}

func applyAutomaticPlan(in analysis.ContextInput, plan Plan) Plan {
	for _, enrich := range automaticPlanEnrichers {
		enrich(in, &plan)
	}
	return plan
}

func addRemoteIPLink(in analysis.ContextInput, plan *Plan) {
	if len(in.Alerts) == 0 {
		return
	}
	target, ok := in.Alerts[0].RemoteIP()
	if !ok {
		return
	}

	for _, existing := range plan.Links {
		if existing.Target == target {
			return
		}
	}
	plan.Links = append(plan.Links, LinkTarget{Target: target})
	sort.Slice(plan.Links, func(i, j int) bool {
		return plan.Links[i].Target < plan.Links[j].Target
	})
}
