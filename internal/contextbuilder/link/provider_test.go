package link

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// TestNewUsesHardcodedBaseURLByDefault mirrors configuration/vdu: production
// wiring passes no base, so the package const must win.
func TestNewUsesHardcodedBaseURLByDefault(t *testing.T) {
	p := New(Options{})
	if p.baseURL != baseURL {
		t.Errorf("New(Options{}).baseURL = %q, want hardcoded %q", p.baseURL, baseURL)
	}
	if baseURL != "http://api/v1/probe/ping" {
		t.Errorf("hardcoded baseURL changed to %q; update this guard", baseURL)
	}
	if p.probeTimeout != DefaultTimeout {
		t.Errorf("probeTimeout = %v, want %v", p.probeTimeout, DefaultTimeout)
	}
	wantRequestTimeout := time.Duration(linkCount)*DefaultTimeout + requestTimeoutGrace
	if p.requestTimeout != wantRequestTimeout {
		t.Errorf("requestTimeout = %v, want %v", p.requestTimeout, wantRequestTimeout)
	}
}

func TestNewDerivesRequestTimeoutFromProbeTimeout(t *testing.T) {
	const probeTimeout = 1500 * time.Millisecond
	p := New(Options{Timeout: probeTimeout})

	want := time.Duration(linkCount)*probeTimeout + requestTimeoutGrace
	if p.requestTimeout != want {
		t.Fatalf("requestTimeout = %v, want %v", p.requestTimeout, want)
	}
	if p.requestTimeout <= p.probeTimeout {
		t.Fatalf("requestTimeout = %v, must exceed probeTimeout %v", p.requestTimeout, p.probeTimeout)
	}
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"status object", `{"status":"SUCCESS"}`, "SUCCESS", false},
		// Whitespace around the object is fine; trailing JSON is not.
		{"whitespace padding", "  {\"status\":\"FAILED\"}  ", "FAILED", false},
		{"empty status rejected", `{"status":""}`, "", true},
		{"missing status rejected", `{"other":"x"}`, "", true},
		{"status must be a string", `{"status":5}`, "", true},
		{"trailing content rejected", `{"status":"SUCCESS"}{}`, "", true},
		{"not an object rejected", `["SUCCESS"]`, "", true},
		{"bare string no longer accepted", `"SUCCESS"`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStatus([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseStatus(%q) err = nil, want non-nil", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStatus(%q) err = %v, want nil", tc.body, err)
			}
			if got != tc.want {
				t.Errorf("parseStatus(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestFetchLinks(t *testing.T) {
	// Capture the method + JSON body the provider POSTs so the request contract
	// (vnfcPath, target, count, timeoutMs) is asserted, not just the response.
	var gotMethod, gotPath string
	var reqBody linkRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "SUCCESS"})
	}))
	t.Cleanup(srv.Close)

	const timeout = 1500 * time.Millisecond
	p := New(Options{
		Client:         srv.Client(),
		Timeout:        timeout,
		RequestTimeout: 100 * time.Millisecond,
		Clock:          contextbuilder.ClockFunc(func() time.Time { return time.Time{} }),
		// Point the provider at the test server; the per-entry target carries no URL.
		BaseURL: srv.URL + "/api/v1/probe/ping",
	})
	target := contextbuilder.LinkTarget{Target: "remote.dns.srv_1"}

	res, err := p.FetchLinks(context.Background(), "ims.vdu_cs_logic.vnfc_cs_logic_1", []contextbuilder.LinkTarget{target})
	if err != nil {
		t.Fatalf("FetchLinks err = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/probe/ping" {
		t.Errorf("request path = %q, want /api/v1/probe/ping", gotPath)
	}
	if reqBody.VNFCPath != "ims.vdu_cs_logic.vnfc_cs_logic_1" {
		t.Errorf("body.vnfcPath = %q, want ims.vdu_cs_logic.vnfc_cs_logic_1", reqBody.VNFCPath)
	}
	if reqBody.Target != "remote.dns.srv_1" {
		t.Errorf("body.target = %q, want remote.dns.srv_1", reqBody.Target)
	}
	if reqBody.Count != linkCount {
		t.Errorf("body.count = %d, want %d", reqBody.Count, linkCount)
	}
	if wantMs := int(timeout / time.Millisecond); reqBody.TimeoutMs != wantMs {
		t.Errorf("body.timeoutMs = %d, want %d", reqBody.TimeoutMs, wantMs)
	}

	if len(res.Entries) != 1 || len(res.Missing) != 0 {
		t.Fatalf("entries=%d missing=%d, want 1 entry 0 missing", len(res.Entries), len(res.Missing))
	}
	e := res.Entries[0]
	if e.Target != "remote.dns.srv_1" || e.Status != "SUCCESS" {
		t.Errorf("entry = %+v, want Target=remote.dns.srv_1 Status=SUCCESS", e)
	}
}

func TestFetchLinksLogsTargetFailure(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	p := New(Options{
		Client:  srv.Client(),
		BaseURL: srv.URL + "/api/v1/probe/ping",
		Logger:  logger,
	})
	res, err := p.FetchLinks(context.Background(), "ims.vdu.vnfc_1", []contextbuilder.LinkTarget{
		{Target: "remote.peer"},
	})
	if err != nil {
		t.Fatalf("FetchLinks err = %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
		t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonHTTPStatus)
	}

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatalf("decode log: %v; log=%q", err, logs.String())
	}
	checks := map[string]any{
		"msg":         "link provider failed",
		"provider":    analysis.ProviderLink,
		"vnfc_path":   "ims.vdu.vnfc_1",
		"target":      "remote.peer",
		"reason":      analysis.ReasonHTTPStatus,
		"http_status": float64(http.StatusBadGateway),
	}
	for key, want := range checks {
		if got := record[key]; got != want {
			t.Errorf("log[%q] = %#v, want %#v", key, got, want)
		}
	}
}

func TestFetchLinksMissingReasons(t *testing.T) {
	clock := contextbuilder.ClockFunc(func() time.Time { return time.Time{} })

	t.Run("http status maps to HTTP_STATUS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/api/v1/probe/ping"})
		res, err := p.FetchLinks(context.Background(), "v", []contextbuilder.LinkTarget{
			{Target: "x"},
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
			t.Fatalf("missing = %+v, want one MissingContext reason=%s", res.Missing, analysis.ReasonHTTPStatus)
		}
		if res.Missing[0].Entity != "x" || res.Missing[0].Key != "" {
			t.Errorf("missing = %+v, want Entity=x empty Key", res.Missing[0])
		}
	})

	t.Run("invalid json maps to INVALID_JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/api/v1/probe/ping"})
		res, _ := p.FetchLinks(context.Background(), "v", []contextbuilder.LinkTarget{
			{Target: "x"},
		})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonInvalidJSON {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonInvalidJSON)
		}
	})

	t.Run("empty body maps to EMPTY_BODY", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/api/v1/probe/ping"})
		res, _ := p.FetchLinks(context.Background(), "v", []contextbuilder.LinkTarget{
			{Target: "x"},
		})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonEmptyBody {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonEmptyBody)
		}
	})

	t.Run("unreachable maps to REQUEST_FAILED", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // shut down so Dial fails

		p := New(Options{Client: srv.Client(), Clock: clock, BaseURL: srv.URL + "/api/v1/probe/ping"})
		res, _ := p.FetchLinks(context.Background(), "v", []contextbuilder.LinkTarget{
			{Target: "x"},
		})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonRequestFailed {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonRequestFailed)
		}
	})

	t.Run("timeout maps to TIMEOUT", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{
			Timeout:        time.Second,
			RequestTimeout: 5 * time.Millisecond,
			Client:         srv.Client(),
			Clock:          clock,
			BaseURL:        srv.URL + "/api/v1/probe/ping",
		})
		res, _ := p.FetchLinks(context.Background(), "v", []contextbuilder.LinkTarget{
			{Target: "x"},
		})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonTimeout {
			t.Fatalf("missing = %+v, want reason=%s", res.Missing, analysis.ReasonTimeout)
		}
	})
}
