package configuration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

var fixedNow = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

func fixedClock() contextbuilder.Clock {
	return contextbuilder.ClockFunc(func() time.Time { return fixedNow })
}

// handlers maps a path to what the fixture API answers with.
func newServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, handler := range handlers {
		mux.HandleFunc(path, handler)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func body(payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(payload))
	}
}

func status(code int, payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(payload))
	}
}

func target(srv *httptest.Server, key string) contextbuilder.ConfigurationTarget {
	return contextbuilder.ConfigurationTarget{
		Path: "ims.vdu_sb_logic.vnfc_sb_logic_1",
		Key:  key,
		URL:  srv.URL + "/" + key,
	}
}

func TestFetchConfigurationValues(t *testing.T) {
	srv := newServer(t, map[string]http.HandlerFunc{
		"/number":  body(`5`),
		"/string":  body(`"enabled"`),
		"/boolean": body(`true`),
		"/null":    body(`null`),
		"/object":  body(`{"limit": 3, "mode": "strict"}`),
		"/array":   body(`[1, 2, 3]`),
		"/spaced":  body("\n  42\n "),
	})

	p := New(Options{Clock: fixedClock()})
	targets := []contextbuilder.ConfigurationTarget{
		target(srv, "number"), target(srv, "string"), target(srv, "boolean"),
		target(srv, "null"), target(srv, "object"), target(srv, "array"),
		target(srv, "spaced"),
	}

	res, err := p.FetchConfiguration(context.Background(), targets)
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("missing = %+v", res.Missing)
	}
	if len(res.Entries) != len(targets) {
		t.Fatalf("entries = %d, want %d", len(res.Entries), len(targets))
	}

	want := map[string]any{
		"number":  float64(5),
		"string":  "enabled",
		"boolean": true,
		"null":    nil,
		"object":  map[string]any{"limit": float64(3), "mode": "strict"},
		"array":   []any{float64(1), float64(2), float64(3)},
		"spaced":  float64(42),
	}
	for _, entry := range res.Entries {
		if !reflect.DeepEqual(entry.Value, want[entry.Key]) {
			t.Errorf("%s = %#v, want %#v", entry.Key, entry.Value, want[entry.Key])
		}
		if !entry.ReadAt.Equal(fixedNow) {
			t.Errorf("%s read_at = %s, want the injected clock's %s", entry.Key, entry.ReadAt, fixedNow)
		}
		if entry.URL == "" || entry.Path == "" {
			t.Errorf("%s lost its provenance: %+v", entry.Key, entry)
		}
	}
}

func TestFetchConfigurationFailureReasons(t *testing.T) {
	srv := newServer(t, map[string]http.HandlerFunc{
		"/not_found":  status(http.StatusNotFound, `{"error": "no such key"}`),
		"/server_err": status(http.StatusInternalServerError, `{"error": "boom"}`),
		// A valid JSON body behind a non-2xx is an error page, not a value.
		"/error_page": status(http.StatusBadGateway, `42`),
		"/empty":      status(http.StatusOK, ``),
		"/whitespace": body("   \n\t "),
		"/malformed":  body(`{oops`),
		"/trailing":   body(`1 2`),
		"/truncated":  body(`{"a": `),
	})

	p := New(Options{Clock: fixedClock()})
	cases := map[string]string{
		"not_found":  analysis.ReasonHTTPStatus,
		"server_err": analysis.ReasonHTTPStatus,
		"error_page": analysis.ReasonHTTPStatus,
		"empty":      analysis.ReasonEmptyBody,
		"whitespace": analysis.ReasonEmptyBody,
		"malformed":  analysis.ReasonInvalidJSON,
		"trailing":   analysis.ReasonInvalidJSON,
		"truncated":  analysis.ReasonInvalidJSON,
	}

	var targets []contextbuilder.ConfigurationTarget
	for key := range cases {
		targets = append(targets, target(srv, key))
	}

	res, err := p.FetchConfiguration(context.Background(), targets)
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries = %+v, none should have resolved", res.Entries)
	}
	if len(res.Missing) != len(cases) {
		t.Fatalf("missing = %d, want %d", len(res.Missing), len(cases))
	}
	for _, m := range res.Missing {
		if m.Provider != analysis.ProviderConfiguration {
			t.Errorf("%s provider = %q", m.Key, m.Provider)
		}
		if m.Entity != "ims.vdu_sb_logic.vnfc_sb_logic_1" {
			t.Errorf("%s entity = %q", m.Key, m.Entity)
		}
		if want := cases[m.Key]; m.Reason != want {
			t.Errorf("%s reason = %q, want %q", m.Key, m.Reason, want)
		}
	}
}

func TestFetchConfigurationUnreachableHost(t *testing.T) {
	p := New(Options{Clock: fixedClock(), Timeout: 500 * time.Millisecond})
	// Port 0 is never listening, so this fails to dial rather than timing out.
	res, err := p.FetchConfiguration(context.Background(), []contextbuilder.ConfigurationTarget{{
		Path: "ims.vdu_a.vnfc_a_1", Key: "k", URL: "http://127.0.0.1:0/k",
	}})
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonRequestFailed {
		t.Fatalf("missing = %+v, want one REQUEST_FAILED", res.Missing)
	}
}

func TestFetchConfigurationTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv := newServer(t, map[string]http.HandlerFunc{
		"/slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
		"/fast": body(`1`),
	})

	p := New(Options{Clock: fixedClock(), Timeout: 100 * time.Millisecond})
	res, err := p.FetchConfiguration(context.Background(), []contextbuilder.ConfigurationTarget{
		target(srv, "slow"), target(srv, "fast"),
	})
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}

	// Partial success: one target timing out must not cost the others.
	if len(res.Entries) != 1 || res.Entries[0].Key != "fast" {
		t.Errorf("entries = %+v, want the fast target only", res.Entries)
	}
	if len(res.Missing) != 1 || res.Missing[0].Key != "slow" || res.Missing[0].Reason != analysis.ReasonTimeout {
		t.Errorf("missing = %+v, want one TIMEOUT for slow", res.Missing)
	}
}

// A caller deadline shorter than the per-call timeout has to win, and it does
// so because the call context is derived from the caller's.
func TestFetchConfigurationCallerDeadlineWins(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv := newServer(t, map[string]http.HandlerFunc{
		"/slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
	})

	p := New(Options{Clock: fixedClock(), Timeout: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := p.FetchConfiguration(ctx, []contextbuilder.ConfigurationTarget{target(srv, "slow")})
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("waited %s, the per-call timeout overrode the caller's deadline", elapsed)
	}
	if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonTimeout {
		t.Errorf("missing = %+v", res.Missing)
	}
}

func TestFetchConfigurationCallerCancelled(t *testing.T) {
	srv := newServer(t, map[string]http.HandlerFunc{"/k": body(`1`)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := New(Options{Clock: fixedClock()})
	if _, err := p.FetchConfiguration(ctx, []contextbuilder.ConfigurationTarget{target(srv, "k")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// Results follow the target order regardless of which call finishes first,
// because each goroutine writes only its own slot.
func TestFetchConfigurationPreservesTargetOrder(t *testing.T) {
	var mu sync.Mutex
	arrivals := 0

	srv := newServer(t, map[string]http.HandlerFunc{
		// The first target is deliberately the slowest, so completion order is
		// the reverse of target order.
		"/a": func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			arrivals++
			mu.Unlock()
			time.Sleep(80 * time.Millisecond)
			w.Write([]byte(`"a"`))
		},
		"/b": body(`"b"`),
		"/c": body(`"c"`),
	})

	p := New(Options{Clock: fixedClock()})
	targets := []contextbuilder.ConfigurationTarget{target(srv, "a"), target(srv, "b"), target(srv, "c")}

	res, err := p.FetchConfiguration(context.Background(), targets)
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	got := []string{res.Entries[0].Key, res.Entries[1].Key, res.Entries[2].Key}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entry order = %v, want %v", got, want)
	}
	if arrivals != 1 {
		t.Errorf("slow handler was entered %d times", arrivals)
	}
}

// Every target is fetched concurrently: a barrier that only opens once all of
// them have arrived would never open under sequential calls.
func TestFetchConfigurationIsConcurrent(t *testing.T) {
	const n = 8

	var wg sync.WaitGroup
	wg.Add(n)
	all := make(chan struct{})
	go func() {
		wg.Wait()
		close(all)
	}()

	srv := newServer(t, map[string]http.HandlerFunc{})
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wg.Done()
		select {
		case <-all:
		case <-time.After(5 * time.Second):
		}
		w.Write([]byte(`1`))
	})

	var targets []contextbuilder.ConfigurationTarget
	for i := 0; i < n; i++ {
		targets = append(targets, target(srv, string(rune('a'+i))))
	}

	p := New(Options{Clock: fixedClock(), Timeout: 10 * time.Second})
	res, err := p.FetchConfiguration(context.Background(), targets)
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if len(res.Entries) != n {
		t.Fatalf("entries = %d, want %d; the calls did not overlap", len(res.Entries), n)
	}
}

func TestFetchConfigurationNoTargets(t *testing.T) {
	p := New(Options{Clock: fixedClock()})
	res, err := p.FetchConfiguration(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchConfiguration() error = %v", err)
	}
	if len(res.Entries) != 0 || len(res.Missing) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
}

func TestNewDefaults(t *testing.T) {
	p := New(Options{})
	if p.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", p.timeout, DefaultTimeout)
	}
	if p.client == nil || p.clock == nil {
		t.Error("New() left a collaborator nil")
	}
}
