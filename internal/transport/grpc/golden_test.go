package grpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"re/internal/analysis"
)

// The response goldens under testdata/engine/ are produced here rather than by
// the database-backed end-to-end tests, and the split is deliberate.
//
// Each scenario's two stage outputs are already pinned by goldens of their own:
// testdata/context_builder/ holds what the Context Builder produced and
// testdata/rca/ holds what the rule engine concluded from it, both verified by
// explicit assertions in their own packages. Feeding those through the real
// Analysis Service and the real mapper yields the response the engine must
// return, without a database in the loop.
//
// The end-to-end tests in cmd/engine then compare the whole PostgreSQL + HTTP +
// GRL path against the same files. A disagreement means the wiring produces
// something the two stages, on their own, do not — which is exactly the class
// of bug an end-to-end test exists to catch, and it could not be caught by a
// golden the same run had just written.

type goldenScenario struct {
	name          string // testdata/engine/<name>
	contextGolden string // testdata/context_builder/<file>
	rcaGolden     string // testdata/rca/<dir>/result.json
}

var goldenScenarios = []goldenScenario{
	{name: "sipgw_link_down", contextGolden: "sipgw_link_down.json", rcaGolden: "link_to_sipgw_down"},
	{name: "diagw_link_down", contextGolden: "diagw_link_down.json", rcaGolden: "link_to_diagw_down"},
	{name: "tps_overloaded", contextGolden: "tps_overloaded.json", rcaGolden: "tps_overloaded"},
}

// stageStub replays one scenario's recorded stage outputs.
type stageStub struct {
	snapshot analysis.ContextSnapshot
	rca      analysis.RCAResult
}

func (s stageStub) Build(context.Context, analysis.ContextInput) (analysis.ContextSnapshot, error) {
	return s.snapshot, nil
}

func (s stageStub) Analyze(context.Context, analysis.ContextSnapshot) (analysis.RCAResult, error) {
	return s.rca, nil
}

func TestEngineResponseGolden(t *testing.T) {
	for _, sc := range goldenScenarios {
		t.Run(sc.name, func(t *testing.T) {
			snapshot := loadJSON[analysis.ContextSnapshot](t,
				filepath.Join("..", "..", "..", "testdata", "context_builder", sc.contextGolden))
			rca := loadJSON[analysis.RCAResult](t,
				filepath.Join("..", "..", "..", "testdata", "rca", sc.rcaGolden, "result.json"))

			stub := stageStub{snapshot: snapshot, rca: rca}
			service, err := analysis.NewService(analysis.ServiceOptions{Context: stub, RCA: stub})
			if err != nil {
				t.Fatal(err)
			}

			// The request is the snapshot's own recorded input, so the golden
			// echoes the same request_id and incident an end-to-end run would.
			result, err := service.AnalyzeIncident(context.Background(), snapshot.Input)
			if err != nil {
				t.Fatalf("AnalyzeIncident: %v", err)
			}

			resp, err := responseToPB(result)
			if err != nil {
				t.Fatalf("responseToPB: %v", err)
			}

			assertEngineGolden(t, sc.name, canonicalJSON(t, resp))
		})
	}
}

func TestEngineResponseGoldenIsInternallyConsistent(t *testing.T) {
	// Two invariants the goldens have to satisfy no matter what the scenarios
	// concluded. They are asserted separately from the byte comparison so that
	// regenerating a golden cannot quietly bless a response that breaks them.
	for _, sc := range goldenScenarios {
		t.Run(sc.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "testdata", "engine", sc.name, "response.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (run with RE_UPDATE_GOLDEN=1 to create it)", path, err)
			}

			var doc struct {
				Status struct {
					Overall string `json:"overall"`
					Context string `json:"context"`
					Rca     string `json:"rca"`
				} `json:"status"`
				Meta struct {
					ContextStatus string `json:"contextStatus"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}

			// meta.context_status restates status.context; letting them
			// disagree would give a caller two answers to one question.
			if doc.Meta.ContextStatus != doc.Status.Context {
				t.Errorf("meta.context_status = %q, status.context = %q",
					doc.Meta.ContextStatus, doc.Status.Context)
			}
			// Overall is the RCA status: the rule engine already lowered
			// itself to PARTIAL if the context was incomplete, so a second
			// opinion here could only disagree with the one that had the
			// evidence.
			if doc.Status.Overall != doc.Status.Rca {
				t.Errorf("status.overall = %q, status.rca = %q", doc.Status.Overall, doc.Status.Rca)
			}
		})
	}
}

// --- helpers -------------------------------------------------------------

// canonicalJSON renders a protobuf message as stable bytes.
//
// protojson deliberately varies its whitespace between runs to discourage
// exactly this kind of comparison, so its output is re-encoded through
// encoding/json, which fixes both the spacing and the key order. The response
// carries no timestamp and no latency, so there is nothing else to normalise.
//
// cmd/engine holds an identical copy: the two live in different packages and a
// shared helper would have to be exported from production code purely for
// tests. They must stay byte-for-byte equivalent, which the end-to-end
// comparison against these same goldens enforces.
func canonicalJSON(t *testing.T, msg proto.Message) []byte {
	t.Helper()

	raw, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatalf("canonicalise: %v", err)
	}
	encoded, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func loadJSON[T any](t *testing.T, path string) T {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func assertEngineGolden(t *testing.T, scenario string, encoded []byte) {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "testdata", "engine", scenario)
	path := filepath.Join(dir, "response.json")

	if os.Getenv("RE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with RE_UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if string(encoded) != string(want) {
		t.Errorf("response does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, encoded)
	}
}
