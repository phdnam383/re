package metric

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// TestNewUsesHardcodedBaseURLByDefault mirrors configuration/vdu/link: production
// wiring passes no base, so the package const must win.
func TestNewUsesHardcodedBaseURLByDefault(t *testing.T) {
	p := New(Options{})
	if p.baseURL != baseURL {
		t.Errorf("New(Options{}).baseURL = %q, want hardcoded %q", p.baseURL, baseURL)
	}
	if baseURL != "http://metrics" {
		t.Errorf("hardcoded baseURL changed to %q; update this guard", baseURL)
	}
}

func TestVduPathOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ims.vdu_cs_logic.vnfc_cs_logic_1", "ims.vdu_cs_logic"},
		{"ims.vdu_cs_logic", "ims"},
		{"barelabel", "barelabel"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := vduPathOf(tc.in); got != tc.want {
			t.Errorf("vduPathOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeValue(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantValue any
		wantErr   bool
	}{
		{"number", `{"value":42}`, float64(42), false},
		{"string", `{"value":"INFO"}`, "INFO", false},
		{"bool", `{"value":true}`, true, false},
		// An explicit JSON null value is a value (nil), not a failure.
		{"null value is nil not error", `{"value":null}`, nil, false},
		{"float", `{"value":12.5}`, float64(12.5), false},
		// Extra response fields (path, unit, ...) are ignored.
		{"extra fields ignored", `{"path":"x","unit":"count","value":42}`, float64(42), false},
		{"whitespace padding ok", "  {\"value\":1}  ", float64(1), false},

		{"missing value rejected", `{"path":"x"}`, nil, true},
		{"not an object: bare number", `42`, nil, true},
		{"not an object: bare string", `"x"`, nil, true},
		{"not an object: array", `[1,2]`, nil, true},
		{"malformed json", `{not json`, nil, true},
		{"trailing content rejected", `{"value":1}{}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeValue([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeValue(%q) err = nil, want non-nil", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeValue(%q) err = %v, want nil", tc.body, err)
			}
			if !equalValue(got, tc.wantValue) {
				t.Errorf("decodeValue(%q) = %#v, want %#v", tc.body, got, tc.wantValue)
			}
		})
	}
}

func TestFetchMetrics(t *testing.T) {
	// Capture method, path, and query the provider GETs, so the request contract
	// (vdu-path.metric-name path + vnfc_path query) is asserted, not just the response.
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"value": 7})
	}))
	t.Cleanup(srv.Close)

	p := New(Options{
		Client:  srv.Client(),
		Timeout: 1500 * time.Millisecond,
		Clock:   contextbuilder.ClockFunc(func() time.Time { return time.Time{} }),
		BaseURL: srv.URL + "/metrics",
	})

	res, err := p.FetchMetrics(context.Background(), "ims.vdu_cs_logic.vnfc_cs_logic_1", []string{"pm_process_count"})
	if err != nil {
		t.Fatalf("FetchMetrics err = %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if wantPath := "/metrics/ims.vdu_cs_logic.pm_process_count"; gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if wantQuery := "vnfc_path=ims.vdu_cs_logic.vnfc_cs_logic_1"; gotQuery != wantQuery {
		t.Errorf("request query = %q, want %q", gotQuery, wantQuery)
	}

	if len(res.Entries) != 1 || len(res.Missing) != 0 {
		t.Fatalf("entries=%d missing=%d, want 1 entry 0 missing", len(res.Entries), len(res.Missing))
	}
	e := res.Entries[0]
	if e.Name != "pm_process_count" {
		t.Errorf("entry.Name = %q, want pm_process_count", e.Name)
	}
	if !equalValue(e.Value, float64(7)) {
		t.Errorf("entry.Value = %#v, want 7", e.Value)
	}
}

func TestFetchMetricsMissingReasons(t *testing.T) {
	clock := contextbuilder.ClockFunc(func() time.Time { return time.Time{} })

	t.Run("http status maps to HTTP_STATUS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/metrics"})
		res, err := p.FetchMetrics(context.Background(), "ims.vdu_cs_logic.vnfc_cs_logic_1", []string{"pm_atom_count"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
			t.Fatalf("missing = %+v, want one MissingContext reason=%s", res.Missing, analysis.ReasonHTTPStatus)
		}
		if res.Missing[0].Entity != "ims.vdu_cs_logic.vnfc_cs_logic_1" || res.Missing[0].Key != "pm_atom_count" {
			t.Errorf("missing = %+v, want Entity=ims.vdu_cs_logic.vnfc_cs_logic_1 Key=pm_atom_count", res.Missing[0])
		}
	})

	t.Run("invalid json maps to INVALID_JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/metrics"})
		res, _ := p.FetchMetrics(context.Background(), "v.vnfc_1", []string{"x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonInvalidJSON {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonInvalidJSON)
		}
	})

	t.Run("empty body maps to EMPTY_BODY", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/metrics"})
		res, _ := p.FetchMetrics(context.Background(), "v.vnfc_1", []string{"x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonEmptyBody {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonEmptyBody)
		}
	})

	t.Run("unreachable maps to REQUEST_FAILED", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // shut down so Dial fails

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/metrics"})
		res, _ := p.FetchMetrics(context.Background(), "v.vnfc_1", []string{"x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonRequestFailed {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonRequestFailed)
		}
	})

	t.Run("timeout maps to TIMEOUT", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Timeout: 5 * time.Millisecond, Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/metrics"})
		res, _ := p.FetchMetrics(context.Background(), "v.vnfc_1", []string{"x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonTimeout {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonTimeout)
		}
	})
}

// equalValue compares decoded JSON values (nil, float64, string, bool).
func equalValue(a, b any) bool {
	switch bv := b.(type) {
	case nil:
		return a == nil
	case float64, string, bool:
		return a == bv
	}
	return false
}
