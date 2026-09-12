package vdu

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"re/internal/analysis"
)

const sampleResponse = `{
  "path": "ims.vdu_dev_sb_dia",
  "name": "dev-sb-dia",
  "type": "DIAGW",
  "namespace": "dev-sb",
  "workload": "StatefulSet",
  "instances": 1,
  "selector": "app=sb-dia",
  "updatedAt": "2026-08-20T10:00:00Z",
  "vnfcs": [
    {
      "id": "ims.vdu_dev_sb_dia.vnfc_dev_sb_dia_1",
      "podName": "sb-dia-6ddc9bb8f5-jlx59",
      "status": "RUNNING",
      "createdAt": "2026-08-22T08:30:00Z",
      "updatedAt": "2026-08-22T08:36:00Z"
    }
  ]
}`

// newProvider returns a Provider whose client talks to srv.
func newProvider(t *testing.T, srv *httptest.Server, timeout time.Duration) *Provider {
	t.Helper()
	return New(Options{
		Client:  srv.Client(),
		BaseURL: srv.URL,
		Timeout: timeout,
	})
}

func TestFetchVDUs(t *testing.T) {
	// Capture method + path so the GET <base>/<path> contract is asserted, not
	// just the response body.
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	t.Cleanup(srv.Close)

	p := newProvider(t, srv, 0)
	path := "ims.vdu_dev_sb_dia"

	res, err := p.FetchVDUs(context.Background(), []string{path})
	if err != nil {
		t.Fatalf("FetchVDUs err = %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if wantPath := "/ims.vdu_dev_sb_dia"; gotPath != wantPath {
		t.Errorf("request path = %q, want %q (raw dots, no encoding)", gotPath, wantPath)
	}

	if len(res.VDUs) != 1 || len(res.VNFCs) != 1 || len(res.Missing) != 0 {
		t.Fatalf("vdus=%d vnfcs=%d missing=%d, want 1/1/0", len(res.VDUs), len(res.VNFCs), len(res.Missing))
	}

	vdu := res.VDUs[0]
	if vdu.Path != "ims.vdu_dev_sb_dia" || vdu.Name != "dev-sb-dia" ||
		vdu.Type != "DIAGW" || vdu.Namespace != "dev-sb" ||
		vdu.Workload != "StatefulSet" || vdu.Instances != 1 || vdu.Selector != "app=sb-dia" {
		t.Errorf("vdu = %+v, want mapped DTO fields", vdu)
	}
	wantVDUUpdated, _ := time.Parse(time.RFC3339, "2026-08-20T10:00:00Z")
	if vdu.UpdatedAt == nil || !vdu.UpdatedAt.Equal(wantVDUUpdated) {
		t.Errorf("vdu UpdatedAt = %v, want REST updatedAt %v", vdu.UpdatedAt, wantVDUUpdated)
	}

	vnfc := res.VNFCs[0]
	wantCreated, _ := time.Parse(time.RFC3339, "2026-08-22T08:30:00Z")
	wantUpdated, _ := time.Parse(time.RFC3339, "2026-08-22T08:36:00Z")
	if vnfc.Path != "ims.vdu_dev_sb_dia.vnfc_dev_sb_dia_1" {
		t.Errorf("vnfc Path = %q, want the response id", vnfc.Path)
	}
	// VDUPath comes from the path requested, not the response — mirrors the
	// Postgres provider's JOIN on the parent VDU.
	if vnfc.VDUPath != path {
		t.Errorf("vnfc VDUPath = %q, want requested path %q", vnfc.VDUPath, path)
	}
	if vnfc.Name != "sb-dia-6ddc9bb8f5-jlx59" || vnfc.Status != "RUNNING" {
		t.Errorf("vnfc = %+v, want podName/status mapped", vnfc)
	}
	// CreatedAt and UpdatedAt are independent REST fields.
	if vnfc.CreatedAt == nil || !vnfc.CreatedAt.Equal(wantCreated) {
		t.Errorf("vnfc CreatedAt = %v, want REST createdAt %v", vnfc.CreatedAt, wantCreated)
	}
	if vnfc.UpdatedAt == nil || !vnfc.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("vnfc UpdatedAt = %v, want REST updatedAt %v", vnfc.UpdatedAt, wantUpdated)
	}
}

func TestFetchVDUsEmptyPathsReturnsEmpty(t *testing.T) {
	p := New(Options{})
	res, err := p.FetchVDUs(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.VDUs) != 0 || len(res.VNFCs) != 0 || len(res.Missing) != 0 {
		t.Fatalf("result = %+v, want zero", res)
	}
}

func TestFetchVDUsFansOutPerPath(t *testing.T) {
	// "ok" path resolves, "bad" path 500s. Fan-out must keep each outcome
	// independent: one VDU + its VNFC, one Missing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ims.vdu_ok" {
			_, _ = w.Write([]byte(sampleResponse))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p := newProvider(t, srv, 0)
	res, err := p.FetchVDUs(context.Background(), []string{"ims.vdu_ok", "ims.vdu_bad"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if len(res.VDUs) != 1 {
		t.Fatalf("vdus = %d, want 1 (only the ok path)", len(res.VDUs))
	}
	if len(res.VNFCs) != 1 {
		t.Fatalf("vnfcs = %d, want 1 (VNFCs only for the ok VDU)", len(res.VNFCs))
	}
	if len(res.Missing) != 1 || res.Missing[0].Entity != "ims.vdu_bad" ||
		res.Missing[0].Reason != analysis.ReasonHTTPStatus {
		t.Fatalf("missing = %+v, want one HTTP_STATUS for ims.vdu_bad", res.Missing)
	}
}

// TestFetchVDUsMapsSchemaFields verifies every analysis.VDU/VNFC field declared in
// types.go has a REST counterpart that flows through when the topology service
// returns it (workload, confd, networks, k8sUid, updatedAt included).
func TestFetchVDUsMapsSchemaFields(t *testing.T) {
	const fullResponse = `{
  "path": "ims.vdu_dev_sb_dia",
  "name": "dev-sb-dia",
  "type": "DIAGW",
  "namespace": "dev-sb",
  "workload": "StatefulSet",
  "instances": 2,
  "selector": "app=sb-dia",
  "confdPath": "/etc/confd/sb-dia",
  "confdServer": "confd-svc:8080",
  "updatedAt": "2026-08-20T10:00:00Z",
  "vnfcs": [
    {
      "id": "ims.vdu_dev_sb_dia.vnfc_dev_sb_dia_1",
      "k8sUid": "a1b2c3d4-0001-0000-0000-000000000001",
      "podName": "sb-dia-6ddc9bb8f5-jlx59",
      "status": "RUNNING",
      "createdAt": "2026-08-22T08:30:00Z",
      "updatedAt": "2026-08-22T08:36:00Z",
      "networks": [
        {"interface":"eth0","network":"k8s-pod-network","ips":["10.244.1.73"],"default":true},
        {"interface":"net1","network":"ims-sb/access-net","ips":["10.60.4.21"]}
      ]
    }
  ]
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fullResponse))
	}))
	t.Cleanup(srv.Close)

	p := newProvider(t, srv, 0)
	res, err := p.FetchVDUs(context.Background(), []string{"ims.vdu_dev_sb_dia"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.VDUs) != 1 || len(res.VNFCs) != 1 {
		t.Fatalf("vdus=%d vnfcs=%d, want 1/1", len(res.VDUs), len(res.VNFCs))
	}

	vdu := res.VDUs[0]
	if vdu.Workload != "StatefulSet" {
		t.Errorf("Workload = %q, want mirrored from workload", vdu.Workload)
	}
	if vdu.ConfdPath != "/etc/confd/sb-dia" {
		t.Errorf("ConfdPath = %q, want mirrored from confdPath", vdu.ConfdPath)
	}
	if vdu.ConfdServer != "confd-svc:8080" {
		t.Errorf("ConfdServer = %q, want mirrored from confdServer", vdu.ConfdServer)
	}
	wantUpdated, _ := time.Parse(time.RFC3339, "2026-08-20T10:00:00Z")
	if vdu.UpdatedAt == nil || !vdu.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want REST updatedAt %v", vdu.UpdatedAt, wantUpdated)
	}

	n := res.VNFCs[0]
	if n.K8sUID != "a1b2c3d4-0001-0000-0000-000000000001" {
		t.Errorf("K8sUID = %q, want mirrored from k8sUid", n.K8sUID)
	}
	if len(n.Networks) != 2 {
		t.Fatalf("Networks len = %d, want 2 (mirrored from networks JSONB)", len(n.Networks))
	}
	if n.Networks[0]["interface"] != "eth0" {
		t.Errorf("Networks[0].interface = %v, want eth0", n.Networks[0]["interface"])
	}
	if v, ok := n.Networks[1]["ips"].([]any); !ok || len(v) != 1 || v[0] != "10.60.4.21" {
		t.Errorf("Networks[1].ips = %v, want [\"10.60.4.21\"]", n.Networks[1]["ips"])
	}
}

// TestFetchVDUsOmitsTimestampsWhenAbsent verifies that when the topology service
// omits the timestamp fields, the snapshot leaves them nil (absent from JSON),
// rather than fabricating fetch-time or zero (0001-01-01) values.
func TestFetchVDUsOmitsTimestampsWhenAbsent(t *testing.T) {
	const noTimestamps = `{
  "path": "ims.vdu_dev_sb_dia", "name": "dev-sb-dia", "type": "DIAGW",
  "namespace": "dev-sb", "instances": 1,
  "vnfcs": [{"id":"ims.vdu_dev_sb_dia.vnfc_dev_sb_dia_1","podName":"p","status":"RUNNING"}]
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(noTimestamps))
	}))
	t.Cleanup(srv.Close)

	p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
	res, err := p.FetchVDUs(context.Background(), []string{"ims.vdu_dev_sb_dia"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.VDUs) != 1 || len(res.VNFCs) != 1 {
		t.Fatalf("vdus=%d vnfcs=%d, want 1/1", len(res.VDUs), len(res.VNFCs))
	}
	if res.VDUs[0].UpdatedAt != nil {
		t.Errorf("VDU UpdatedAt = %v, want nil (REST omitted updatedAt)", res.VDUs[0].UpdatedAt)
	}
	if res.VNFCs[0].CreatedAt != nil {
		t.Errorf("VNFC CreatedAt = %v, want nil (REST omitted createdAt)", res.VNFCs[0].CreatedAt)
	}
	if res.VNFCs[0].UpdatedAt != nil {
		t.Errorf("VNFC UpdatedAt = %v, want nil (REST omitted updatedAt)", res.VNFCs[0].UpdatedAt)
	}

	// And the snapshot JSON for such a VDU/VNFC omits the timestamp keys
	// entirely (omitempty on *time.Time kicks in for nil), not 0001-01-01.
	b, err := json.Marshal(res.VDUs[0])
	if err != nil {
		t.Fatalf("marshal vdu: %v", err)
	}
	if bytes.Contains(b, []byte("updated_at")) {
		t.Errorf("VDU JSON = %s, want no updated_at key when absent", b)
	}
}

func TestDecodeVDU(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		wantDTO vduResponse
	}{
		{
			name: "full descriptor",
			body: sampleResponse,
			wantDTO: vduResponse{
				Path: "ims.vdu_dev_sb_dia", Name: "dev-sb-dia", Type: "DIAGW",
				Namespace: "dev-sb", Instances: 1, Selector: "app=sb-dia",
				VNFCs: []vnfcResponse{{
					ID: "ims.vdu_dev_sb_dia.vnfc_dev_sb_dia_1",
					PodName: "sb-dia-6ddc9bb8f5-jlx59", Status: "RUNNING",
					CreatedAt: "2026-08-22T08:30:00Z", UpdatedAt: "2026-08-22T08:36:00Z",
				}},
			},
		},
		{"empty vnfcs array ok", `{"path":"p","instances":2,"vnfcs":[]}`, false, vduResponse{Path: "p", Instances: 2}},
		{"whitespace padding ok", "  {\"path\":\"p\"}  ", false, vduResponse{Path: "p"}},
		{"extra trailing fields ignored by decoder", `{"path":"p","future":"x"}`, false, vduResponse{Path: "p"}},

		{"not an object: bare number", `42`, true, vduResponse{}},
		{"not an object: array", `[]`, true, vduResponse{}},
		{"malformed json", `{not json`, true, vduResponse{}},
		{"trailing content rejected", `{"path":"p"}{}`, true, vduResponse{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeVDU([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeVDU(%q) err = nil, want non-nil", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeVDU(%q) err = %v, want nil", tc.body, err)
			}
			if got.Path != tc.wantDTO.Path || got.Instances != tc.wantDTO.Instances ||
				got.Name != tc.wantDTO.Name || got.Type != tc.wantDTO.Type ||
				got.Namespace != tc.wantDTO.Namespace || got.Selector != tc.wantDTO.Selector {
				t.Errorf("decodeVDU got = %+v, want %+v", got, tc.wantDTO)
			}
			if len(got.VNFCs) != len(tc.wantDTO.VNFCs) {
				t.Fatalf("vnfcs len = %d, want %d", len(got.VNFCs), len(tc.wantDTO.VNFCs))
			}
			for i := range got.VNFCs {
				g, w := got.VNFCs[i], tc.wantDTO.VNFCs[i]
				if g.ID != w.ID || g.PodName != w.PodName || g.Status != w.Status ||
					g.CreatedAt != w.CreatedAt || g.UpdatedAt != w.UpdatedAt {
					t.Errorf("vnfc[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	wantRaw, _ := time.Parse(time.RFC3339, "2026-08-22T08:36:00Z")
	want := &wantRaw
	cases := []struct {
		name string
		in   string
		want *time.Time
	}{
		{"rfc3339", "2026-08-22T08:36:00Z", want},
		{"whitespace trimmed", "  2026-08-22T08:36:00Z  ", want},
		{"empty is nil", "", nil},
		{"garbage is nil", "not-a-time", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTime(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Errorf("parseTime(%q) = %v, want nil", tc.in, *got)
				}
				return
			}
			if got == nil || !got.Equal(*tc.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tc.in, got, *tc.want)
			}
		})
	}
}

func TestFetchVDUsMissingReasons(t *testing.T) {

	t.Run("404 maps to HTTP_STATUS (mirrors siblings)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
			t.Fatalf("missing = %+v, want HTTP_STATUS (not NOT_FOUND)", res.Missing)
		}
		assertEntity(t, res.Missing[0], "ims.vdu_x")
	})

	t.Run("500 maps to HTTP_STATUS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonHTTPStatus {
			t.Fatalf("missing = %+v, want HTTP_STATUS", res.Missing)
		}
	})

	t.Run("empty 200 body maps to EMPTY_BODY", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonEmptyBody {
			t.Fatalf("missing = %+v, want EMPTY_BODY", res.Missing)
		}
	})

	t.Run("malformed json maps to INVALID_JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonInvalidJSON {
			t.Fatalf("missing = %+v, want INVALID_JSON", res.Missing)
		}
	})

	t.Run("unreachable maps to REQUEST_FAILED", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // shut down so Dial fails

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonRequestFailed {
			t.Fatalf("missing = %+v, want REQUEST_FAILED", res.Missing)
		}
		assertEntity(t, res.Missing[0], "ims.vdu_x")
	})

	t.Run("timeout maps to TIMEOUT", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}))
		t.Cleanup(srv.Close)

		p := New(Options{Client: srv.Client(), BaseURL: srv.URL, Timeout: 5 * time.Millisecond})
		res, _ := p.FetchVDUs(context.Background(), []string{"ims.vdu_x"})
		if len(res.Missing) != 1 || res.Missing[0].Reason != analysis.ReasonTimeout {
			t.Fatalf("missing = %+v, want TIMEOUT", res.Missing)
		}
	})
}

// TestFetchVDUsUsesHardcodedBaseURLByDefault documents that production wiring
// (no BaseURL in Options) hits the real topology service endpoint.
func TestFetchVDUsUsesHardcodedBaseURLByDefault(t *testing.T) {
	p := New(Options{})
	if p.baseURL != baseURL {
		t.Errorf("default baseURL = %q, want hardcoded %q", p.baseURL, baseURL)
	}
}

func TestNewDefaults(t *testing.T) {
	p := New(Options{})
	if p.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want DefaultTimeout", p.timeout)
	}
	if p.client == nil {
		t.Error("client = nil, want default http.Client")
	}
}

// Ensure the JSON DTO tags are stable — a drift would silently drop fields the
// topology service returns.
func TestVDUResponseTags(t *testing.T) {
	var dto vduResponse
	if err := json.Unmarshal([]byte(sampleResponse), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Instances != 1 || dto.VNFCs[0].PodName != "sb-dia-6ddc9bb8f5-jlx59" {
		t.Errorf("dto tags drifted: %+v", dto)
	}
}

func assertEntity(t *testing.T, m analysis.MissingContext, wantEntity string) {
	t.Helper()
	if m.Provider != analysis.ProviderVDU {
		t.Errorf("missing Provider = %q, want %q", m.Provider, analysis.ProviderVDU)
	}
	if m.Entity != wantEntity {
		t.Errorf("missing Entity = %q, want %q", m.Entity, wantEntity)
	}
}
