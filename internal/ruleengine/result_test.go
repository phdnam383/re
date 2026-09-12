package ruleengine

import (
	"testing"

	"re/internal/analysis"
)

// newScopedResult returns a Result whose rule scope is fixed — mimicking how
// GRLRuntime.Run wires currentRule for the duration of one rule. This lets a
// test drive Assert -> Recommend* like a real firing rule.
func newScopedResult() *Result {
	r := NewResult()
	r.currentRule = func() string { return "test-rule" }
	return r
}

func wantRootCause(t *testing.T, got []analysis.RootCause, want analysis.RootCause) {
	t.Helper()
	found := false
	for _, rc := range got {
		if rc.Category == want.Category && rc.Role == want.Role && rc.Summary == want.Summary {
			found = true
			if len(rc.Components) != 1 {
				t.Fatalf("root cause %q: got %d components, want 1", want.Category, len(rc.Components))
			}
			compareAction(t, rc.Components[0], want.Components[0])
		}
	}
	if !found {
		t.Fatalf("root cause %q not asserted; got %+v", want.Category, got)
	}
}

func compareAction(t *testing.T, got analysis.Component, want analysis.Component) {
	t.Helper()
	if got.Entity != want.Entity {
		t.Errorf("entity = %q, want %q", got.Entity, want.Entity)
	}
	if got.Action == nil {
		t.Fatal("action is nil")
	}
	if got.Action.Code != want.Action.Code {
		t.Errorf("code = %q, want %q", got.Action.Code, want.Action.Code)
	}
	if got.Action.MOInstance != want.Action.MOInstance {
		t.Errorf("mo_instance = %q, want %q", got.Action.MOInstance, want.Action.MOInstance)
	}
	if got.Action.Op != want.Action.Op {
		t.Errorf("op = %q, want %q", got.Action.Op, want.Action.Op)
	}
	if got.Action.Value != want.Action.Value {
		t.Errorf("value = %v, want %v", got.Action.Value, want.Action.Value)
	}
}

func TestRecommendRestartVNFCAt(t *testing.T) {
	const path = "ims.vdu_sb_logic.vnfc_sb_logic_1"

	t.Run("single instance", func(t *testing.T) {
		r := newScopedResult()
		r.Assert("ERLANG_RESOURCE_EXHAUSTED", "PRIMARY", "node at its process limit")
		r.RecommendRestartVNFCAt(path)

		if err := r.Err(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantRootCause(t, r.RootCauses(), analysis.RootCause{
			Category: "ERLANG_RESOURCE_EXHAUSTED", Role: "PRIMARY",
			Summary: "node at its process limit",
			Components: []analysis.Component{{
				Entity: path,
				Action: &analysis.RecommendedAction{
					Code: "RESTART_VNFC", MOInstance: path, Op: analysis.OpReplace,
				},
			}},
		})
	})

	t.Run("empty path is a rule error", func(t *testing.T) {
		r := newScopedResult()
		r.Assert("ERLANG_RESOURCE_EXHAUSTED", "PRIMARY", "node at its process limit")
		r.RecommendRestartVNFCAt("   ")

		if err := r.Err(); err == nil {
			t.Fatal("expected error for empty path, got nil")
		}
		if got := r.RootCauses(); len(got) == 1 && len(got[0].Components) != 0 {
			t.Fatalf("expected no components, got %v", got[0].Components)
		}
	})

	t.Run("no assert -> no-op, logged error", func(t *testing.T) {
		r := newScopedResult()
		r.RecommendRestartVNFCAt(path)
		if err := r.Err(); err == nil {
			t.Fatal("expected error when no Assert precedes the recommend")
		}
	})
}

func TestRecommendPurgeOldestRows(t *testing.T) {
	const (
		entity = "ims.vdu_cs_logic.vnfc_cs_logic_1"
		table  = "transaction_garbage_timer"
	)

	t.Run("marks rows for removal", func(t *testing.T) {
		r := newScopedResult()
		r.Assert("TABLE_SIZE_OVERLOAD", "PRIMARY", "table at its size limit")
		r.RecommendPurgeOldestRows(entity, table, 26)

		if err := r.Err(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantRootCause(t, r.RootCauses(), analysis.RootCause{
			Category: "TABLE_SIZE_OVERLOAD", Role: "PRIMARY",
			Summary: "table at its size limit",
			Components: []analysis.Component{{
				Entity: entity,
				Action: &analysis.RecommendedAction{
					Code:       "PURGE_OLDEST_ROWS",
					MOInstance: entity + "_" + table,
					Op:         analysis.OpRemove,
					Value:      26,
				},
			}},
		})
	})

	for _, rows := range []int{0, -5} {
		t.Run("non-positive rows error", func(t *testing.T) {
			r := newScopedResult()
			r.Assert("TABLE_SIZE_OVERLOAD", "PRIMARY", "table at its size limit")
			r.RecommendPurgeOldestRows(entity, table, rows)
			if err := r.Err(); err == nil {
				t.Fatalf("expected error for rows=%d, got nil", rows)
			}
		})
	}
}

func TestRecommendNotifyNOC(t *testing.T) {
	const (
		entity  = "ims.vdu_sb_logic.vnfc_sb_logic_1"
		message = "escalate to the transmission team to check the physical path"
	)

	t.Run("attaches a notify action", func(t *testing.T) {
		r := newScopedResult()
		r.Assert("SIPGW_ACCESS_IP_LINK_DOWN", "PRIMARY", "IP path to the access peer is down")
		r.RecommendNotifyNOC(entity, message)

		if err := r.Err(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantRootCause(t, r.RootCauses(), analysis.RootCause{
			Category: "SIPGW_ACCESS_IP_LINK_DOWN", Role: "PRIMARY",
			Summary: "IP path to the access peer is down",
			Components: []analysis.Component{{
				Entity: entity,
				Action: &analysis.RecommendedAction{
					Code:       "NOTIFY_NOC",
					MOInstance: entity,
					Op:         analysis.OpNotify,
					Value:      message,
				},
			}},
		})
	})

	for _, tc := range []struct {
		name    string
		entity  string
		message string
	}{
		{"empty entity", "   ", message},
		{"empty message", entity, "  "},
	} {
		t.Run(tc.name+" is a rule error", func(t *testing.T) {
			r := newScopedResult()
			r.Assert("SIPGW_ACCESS_IP_LINK_DOWN", "PRIMARY", "IP path to the access peer is down")
			r.RecommendNotifyNOC(tc.entity, tc.message)
			if err := r.Err(); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}

	t.Run("no assert -> no-op, logged error", func(t *testing.T) {
		r := newScopedResult()
		r.RecommendNotifyNOC(entity, message)
		if err := r.Err(); err == nil {
			t.Fatal("expected error when no Assert precedes the notify")
		}
	})
}
