package configuration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/contextbuilder/link"
	"re/internal/contextbuilder/metric"
	"re/internal/contextbuilder/vdu"
	"re/internal/ruleengine"
)

func TestDecodeCurrentValue(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantValue any
		wantErr   bool
	}{
		{"number", `{"currentValue":42}`, float64(42), false},
		{"string", `{"currentValue":"INFO"}`, "INFO", false},
		{"bool", `{"currentValue":true}`, true, false},
		// An explicit JSON null currentValue is a value (nil), not a failure:
		// the caller still produces an entry so Ctx.Cfg.Has stays true.
		{"null currentValue is nil not error", `{"currentValue":null}`, nil, false},
		{"object currentValue passes through", `{"currentValue":{"a":1}}`, map[string]any{"a": float64(1)}, false},
		{"array currentValue passes through", `{"currentValue":[1,2]}`, []any{float64(1), float64(2)}, false},
		// Extra descriptor fields (path, binding, valueSchema, …) are ignored.
		{"extra fields ignored", `{"path":"x","binding":{"get":{}},"valueSchema":{"type":"number"},"currentValue":42}`, float64(42), false},
		{"whitespace padding ok", "  {\"currentValue\":1}  ", float64(1), false},

		{"missing currentValue rejected", `{"path":"x","binding":{}}`, nil, true},
		{"not an object: bare number", `42`, nil, true},
		{"not an object: bare string", `"x"`, nil, true},
		{"not an object: array", `[1,2]`, nil, true},
		{"malformed json", `{not json`, nil, true},
		{"trailing content rejected", `{"currentValue":1}{}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeCurrentValue([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeCurrentValue(%q) err = nil, want non-nil", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeCurrentValue(%q) err = %v, want nil", tc.body, err)
			}
			if !equalValue(got, tc.wantValue) {
				t.Errorf("decodeCurrentValue(%q) = %#v, want %#v", tc.body, got, tc.wantValue)
			}
		})
	}
}

// equalValue compares decoded JSON values (nil, float64, string, bool, map, slice).
func equalValue(a, b any) bool {
	switch bv := b.(type) {
	case nil:
		return a == nil
	case float64, string, bool:
		return a == bv
	case map[string]any:
		am, ok := a.(map[string]any)
		if !ok || len(am) != len(bv) {
			return false
		}
		for k, v := range bv {
			if !equalValue(am[k], v) {
				return false
			}
		}
		return true
	case []any:
		as, ok := a.([]any)
		if !ok || len(as) != len(bv) {
			return false
		}
		for i := range bv {
			if !equalValue(as[i], bv[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// TestNewUsesHardcodedBaseURLByDefault mirrors vdu.TestFetchVDUsUsesHardcodedBaseURLByDefault:
// production wiring passes no base, so the package const must win.
func TestNewUsesHardcodedBaseURLByDefault(t *testing.T) {
	p := New(Options{})
	if p.baseURL != baseURL {
		t.Errorf("New(Options{}).baseURL = %q, want hardcoded %q", p.baseURL, baseURL)
	}
	if baseURL != "http://config" {
		t.Errorf("hardcoded baseURL changed to %q; update this guard", baseURL)
	}
}

func TestFetchConfiguration(t *testing.T) {
	// Capture the method + path the provider GETs so the request contract
	// (GET <base>/<path>.<key>) is asserted, not just the response.
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":         "ims.vdu_sb_logic.vnfc_sb_logic_1.log_file_count",
			"valueSchema":  map[string]string{"type": "number"},
			"currentValue": 42,
			"updatedAt":    "2026-07-31T10:20:02Z",
		})
	}))
	t.Cleanup(srv.Close)

	p := New(Options{
		Client:  srv.Client(),
		Clock:   contextbuilder.ClockFunc(func() time.Time { return time.Time{} }),
		BaseURL: srv.URL, // override the hardcoded base to point at the test server
	})
	path := "ims.vdu_sb_logic.vnfc_sb_logic_1"
	key := "log_file_count"

	res, err := p.FetchConfiguration(context.Background(), path, []string{key})
	if err != nil {
		t.Fatalf("FetchConfiguration err = %v", err)
	}

	// Configuration is a GET (unlike the link provider's POST).
	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if wantPath := "/ims.vdu_sb_logic.log_file_count"; gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}

	if len(res.Entries) != 1 || len(res.Missing) != 0 {
		t.Fatalf("entries=%d missing=%d, want 1 entry 0 missing", len(res.Entries), len(res.Missing))
	}
	e := res.Entries[0]
	if e.Value != float64(42) {
		t.Errorf("entry Value = %#v, want float64(42) (the currentValue, not the whole body)", e.Value)
	}
	if e.Key != key {
		t.Errorf("entry Key = %q, want %q", e.Key, key)
	}
	if !e.ReadAt.Equal(time.Time{}) {
		t.Errorf("entry ReadAt = %v, want zero time from injected clock", e.ReadAt)
	}
}

func TestFetchConfigurationMissingReasons(t *testing.T) {
	clock := contextbuilder.ClockFunc(func() time.Time { return time.Time{} })
	path := "ims.vdu_sb_logic.vnfc_sb_logic_1"
	key := "log_file_count"

	// newProvider wires an httptest.Server base; the provider receives the
	// source path separately from the configuration keys.
	newProvider := func(srv *httptest.Server) *Provider {
		return New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL})
	}
	fetch := func(p *Provider) (contextbuilder.ConfigurationResult, error) {
		return p.FetchConfiguration(context.Background(), path, []string{key})
	}

	t.Run("http status maps to HTTP_STATUS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		p := newProvider(srv)
		res, err := fetch(p)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
			t.Fatalf("missing = %+v, want one MissingContext reason=%s", res.Missing, analysis.ReasonHTTPStatus)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})

	t.Run("malformed json maps to INVALID_JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		t.Cleanup(srv.Close)

		p := newProvider(srv)
		res, _ := fetch(p)
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonInvalidJSON {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonInvalidJSON)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})

	t.Run("descriptor missing currentValue maps to INVALID_JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"path":"x","binding":{"get":{}}}`))
		}))
		t.Cleanup(srv.Close)

		p := newProvider(srv)
		res, _ := fetch(p)
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonInvalidJSON {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonInvalidJSON)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})

	t.Run("explicit null currentValue yields an entry, not a missing reason", func(t *testing.T) {
		// Contract: Ctx.Cfg.Has stays true even when the JSON value is null.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"currentValue":null}`))
		}))
		t.Cleanup(srv.Close)

		p := newProvider(srv)
		res, err := fetch(p)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(res.Entries) != 1 || len(res.Missing) != 0 {
			t.Fatalf("entries=%d missing=%d, want 1 entry (nil value) 0 missing", len(res.Entries), len(res.Missing))
		}
		if res.Entries[0].Value != nil {
			t.Errorf("entry Value = %#v, want nil", res.Entries[0].Value)
		}
	})

	t.Run("empty body maps to EMPTY_BODY", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		p := newProvider(srv)
		res, _ := fetch(p)
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonEmptyBody {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonEmptyBody)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})

	t.Run("unreachable maps to REQUEST_FAILED", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // shut down so Dial fails

		p := newProvider(srv)
		res, _ := fetch(p)
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonRequestFailed {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonRequestFailed)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})

	t.Run("timeout maps to TIMEOUT", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Timeout: 5 * time.Millisecond, Client: srv.Client(), Clock: clock, BaseURL: srv.URL})
		res, _ := fetch(p)
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonTimeout {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonTimeout)
		}
		assertEntityKey(t, res.Missing[0], path, key)
	})
}

func assertEntityKey(t *testing.T, m analysis.MissingContext, wantPath, wantKey string) {
	t.Helper()
	if m.Entity != wantPath || m.Key != wantKey {
		t.Errorf("missing = %+v, want Entity=%q Key=%q", m, wantPath, wantKey)
	}
}

type ramProfiles []contextbuilder.ContextProfile

func (p ramProfiles) LoadEnabled(context.Context) ([]contextbuilder.ContextProfile, error) {
	return p, nil
}

func TestRAMConfigurationFlow(t *testing.T) {
	data, err := os.ReadFile("../../../context_profile/OVERLOAD_RAM/SBC_OVERLOAD_RAM.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Name        string
		Description string
		Selector    json.RawMessage
		Providers   json.RawMessage
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	profile, err := contextbuilder.DecodeProfile(document.Name, document.Description, document.Selector, document.Providers)
	if err != nil {
		t.Fatal(err)
	}

	const source = "ims.vdu_sb_logic.vnfc_sb_logic_1"
	const configPath = "ims.vdu_sb_logic"
	values := map[string]any{"log_file_count": 20, "log_file_size": 100, "limit_memory": 4000, "log_level": "DEBUG"}
	var mu sync.Mutex
	requests := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/metrics/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": 0})
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/config/"+configPath+".")
		value, ok := values[key]
		if !ok {
			t.Errorf("unexpected configuration URL: %s", r.URL.String())
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		requests[key]++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"currentValue": value})
	}))
	defer srv.Close()
	b, err := contextbuilder.New(contextbuilder.Options{
		Profiles:      ramProfiles{profile},
		Configuration: New(Options{Client: srv.Client(), BaseURL: srv.URL + "/api/v1/config"}),
		VDU:           vdu.New(vdu.Options{BaseURL: srv.URL}),
		Link:          link.New(link.Options{BaseURL: srv.URL}),
		Metric:        metric.New(metric.Options{Client: srv.Client(), BaseURL: srv.URL + "/api/v1/metrics"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.Build(context.Background(), analysis.ContextInput{
		RequestID: "ram-configuration-flow",
		Alerts:    []analysis.Alert{{SourcePath: source, ProbableCause: "OVERLOAD_RAM", AlertType: "QUALITY_OF_SERVICE_ALERT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != analysis.StatusComplete || len(snap.Configuration) != 4 {
		t.Fatalf("status = %s, configuration = %+v, missing = %+v", snap.Status, snap.Configuration, snap.MissingContext)
	}
	mu.Lock()
	for key := range values {
		if requests[key] != 1 {
			t.Errorf("requests for %s = %d, want 1", key, requests[key])
		}
	}
	mu.Unlock()
	for _, entry := range snap.Configuration {
		if _, ok := values[entry.Key]; !ok {
			t.Errorf("unexpected entry: %+v", entry)
		}
	}
	facts := ruleengine.NewFacts(snap)
	if !facts.Cfg.Has(source, "log_level") || facts.Cfg.Has("ims.vdu_sb_logic.vnfc_other", "log_level") {
		t.Fatal("configuration lookup must remain associated with the matching alert source")
	}

	// Exercise the three configuration rules from the shipped document. Metric
	// rules in the same file are outside this configuration regression test.
	grl, err := os.ReadFile("../../../grule/OVERLOAD_RAM/SBC_OVERLOAD_RAM.grl")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(grl), "rule RAMOverloadLogFileCount")
	if start < 0 {
		t.Fatal("configuration rules not found")
	}
	session, err := ruleengine.NewGRLRuntime().Prepare(analysis.RuleDefinition{
		ID: "ram-configuration", Name: "ram-configuration", Content: string(grl[start:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := ruleengine.NewResult()
	if err := session.Run(context.Background(), facts, out); err != nil {
		t.Fatal(err)
	}
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, cause := range out.RootCauses() {
		got[cause.Category] = true
	}
	want := map[string]bool{
		"HIGH_LOG_FILE_COUNT_CONFIG": true,
		"HIGH_LOG_FILE_SIZE_CONFIG":  true,
		"VERBOSE_LOG_LEVEL_CONFIG":   true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("root cause categories = %v, want %v", got, want)
	}
}
