package ruleengine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"re/internal/analysis"
)

func TestGRLAutomaticRemoteLinkPredicates(t *testing.T) {
	const remoteIP = "10.55.70.37"
	for _, tc := range []struct {
		name     string
		method   string
		status   string
		category string
	}{
		{name: "failed", method: "PingToRemoteFailed", status: linkStatusFailed, category: "REMOTE_FAILED"},
		{name: "success", method: "PingToRemoteSuccess", status: linkStatusSuccess, category: "REMOTE_SUCCESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := analysis.RuleDefinition{
				ID:   "automatic-remote-" + tc.name,
				Name: "automatic remote " + tc.name,
				Content: fmt.Sprintf(`
rule AutomaticRemote "automatic remote probe" salience 1 {
    when
        Ctx.Link.%s()
    then
        Result.Assert("%s", "PRIMARY", "automatic remote probe matched");
}
`, tc.method, tc.category),
				UpdatedAt: time.Unix(0, 0),
			}
			runtime := NewGRLRuntime()
			session, err := runtime.Prepare(rule)
			if err != nil {
				t.Fatalf("prepare GRL: %v", err)
			}

			facts := NewFacts(analysis.ContextSnapshot{
				Input: analysis.ContextInput{Alerts: []analysis.Alert{{
					AdditionalInformation: map[string]any{
						analysis.AdditionalInformationRemoteIP: remoteIP,
					},
				}}},
				Links: []analysis.LinkEntry{{Target: remoteIP, Status: tc.status}},
			})
			result := NewResult()
			if err := session.Run(context.Background(), facts, result); err != nil {
				t.Fatalf("run GRL: %v", err)
			}
			if err := result.Err(); err != nil {
				t.Fatalf("result error: %v", err)
			}
			causes := result.RootCauses()
			if len(causes) != 1 || causes[0].Category != tc.category {
				t.Fatalf("causes = %+v, want category %q", causes, tc.category)
			}
		})
	}
}
