package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"re/internal/profilemanagement"
	"strings"
	"testing"
	"time"
)

type profileMemory struct {
	p      profilemanagement.Profile
	exists bool
}

func (m *profileMemory) Create(_ context.Context, p profilemanagement.Profile) (profilemanagement.Profile, error) {
	if m.exists {
		return p, profilemanagement.ErrConflict
	}
	p.ID = "00000000-0000-0000-0000-000000000001"
	p.UpdatedAt = time.Now()
	m.p = p
	m.exists = true
	return p, nil
}
func (m *profileMemory) Get(context.Context, string) (profilemanagement.Profile, error) {
	if !m.exists {
		return m.p, profilemanagement.ErrNotFound
	}
	return m.p, nil
}
func (m *profileMemory) List(context.Context, profilemanagement.ListFilter) (profilemanagement.ProfilePage, error) {
	if !m.exists {
		return profilemanagement.ProfilePage{}, nil
	}
	return profilemanagement.ProfilePage{Items: []profilemanagement.Profile{m.p}, Total: 1}, nil
}
func (m *profileMemory) Update(_ context.Context, p profilemanagement.Profile, expected time.Time) (profilemanagement.Profile, error) {
	if !expected.Equal(m.p.UpdatedAt) {
		return p, profilemanagement.ErrConflict
	}
	m.p = p
	return p, nil
}
func (m *profileMemory) Delete(context.Context, string) error {
	if !m.exists {
		return profilemanagement.ErrNotFound
	}
	m.exists = false
	return nil
}

func TestProfileLifecycle(t *testing.T) {
	repo := &profileMemory{}
	server := NewServer(":0", &stubService{}, profilemanagement.NewService(repo), nil)
	path := "/api/v1/context-profiles"
	request := func(method, url, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		return w
	}
	w := request("POST", path, `{"name":" example ","selector":{"alert_types":["cpu"],"source_paths":["ims.logic"]},"providers":{"vdu":["ims.logic"],"configuration":["limit"],"metric":["cpu","ram"]}}`, 201)
	idPath := w.Header().Get("Location")
	if idPath != path+"/"+repo.p.ID || !repo.p.Enabled || repo.p.Name != "example" {
		t.Fatal("invalid create defaults")
	}
	request("POST", path, `{"name":"example","selector":{"alert_types":["cpu"]}}`, 409)
	request("PATCH", idPath, `{"providers":{"metric":["new_cpu"]},"enabled":false}`, 200)
	if repo.p.Enabled || len(repo.p.Providers.VDU) != 0 || len(repo.p.Providers.Configuration) != 0 || len(repo.p.Providers.Metric) != 1 || repo.p.Providers.Metric[0] != "new_cpu" || len(repo.p.Selector.SourcePaths) != 1 {
		t.Fatalf("replacement failed: %+v", repo.p)
	}
	request("PATCH", idPath, `{"selector":{"probable_causes":["overload"]}}`, 200)
	if len(repo.p.Selector.AlertTypes) != 0 || len(repo.p.Selector.SourcePaths) != 0 || len(repo.p.Providers.Metric) != 1 {
		t.Fatal("selector merged instead of replaced")
	}
	for _, body := range []string{`{}`, `{"selector":{}}`, `{"selector":null}`, `{"providers":null}`, `{"providers":{"unknown":[]}}`, `{"selector":{"unknown":[]}}`, `{"providers":{"metric":["bad-path"]}}`, `{"selector":{"source_paths":["one"]}}`, `{"providers":[]}`, `{"enabled":null}`, `{} {}`, `null`} {
		request("PATCH", idPath, body, 400)
		if repo.p.Selector.ProbableCauses[0] != "overload" || len(repo.p.Providers.Metric) != 1 {
			t.Fatal("invalid update persisted")
		}
	}
	request("PATCH", idPath, `{"providers":{}}`, 200)
	if len(repo.p.Providers.Metric) != 0 {
		t.Fatal("providers not cleared")
	}
	request("GET", idPath, "", 200)
	request("GET", path+"?enabled=false&limit=1&offset=0", "", 200)
	for _, q := range []string{"limit=0", "offset=-1", "enabled=yes", "limit=1&limit=2", "unknown=1"} {
		request("GET", path+"?"+q, "", 400)
	}
	request("GET", path+"/bad-id", "", 400)
	request("DELETE", idPath, "", 204)
	request("GET", idPath, "", 404)
	request("DELETE", idPath, "", 404)
	w = request("GET", path, "", 200)
	var page struct {
		Items []profileResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatal("empty page must contain []")
	}
}
