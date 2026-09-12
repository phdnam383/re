package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"re/internal/analysis"
	"re/internal/rulemanagement"
	"strings"
	"testing"
)

type stubService struct {
	update rulemanagement.UpdateInput
	create rulemanagement.CreateInput
	err    error
}

func (s *stubService) Create(_ context.Context, in rulemanagement.CreateInput) (rulemanagement.Rule, error) {
	s.create = in
	return rulemanagement.Rule{RuleDefinition: analysis.RuleDefinition{ID: "example", Content: in.Content}}, s.err
}
func (s *stubService) Update(_ context.Context, _ string, in rulemanagement.UpdateInput) (rulemanagement.Rule, error) {
	s.update = in
	return rulemanagement.Rule{}, s.err
}
func (s *stubService) Get(context.Context, string) (rulemanagement.Rule, error) {
	return rulemanagement.Rule{}, s.err
}
func (s *stubService) List(context.Context, rulemanagement.ListFilter) (rulemanagement.RulePage, error) {
	return rulemanagement.RulePage{}, s.err
}
func (s *stubService) Delete(context.Context, string) error { return s.err }

func TestRuleRoutes(t *testing.T) {
	s := &stubService{}
	h := NewServer(":0", s, nil).Handler
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/rules", `{"name":"test","rule_content":"grl","enabled":false}`, 201},
		{"PATCH", "/api/v1/rules/id", `{"enabled":false,"salience":0}`, 200},
		{"GET", "/api/v1/rules/id", "", 200},
		{"GET", "/api/v1/rules?enabled=false&limit=10&offset=0", "", 200},
		{"DELETE", "/api/v1/rules/id", "", 204},
		{"POST", "/api/v1/rules", `{"unknown":1}`, 400},
		{"PATCH", "/api/v1/rules/id", `{"enabled":null}`, 400},
		{"POST", "/api/v1/rules", `{} {}`, 400},
		{"POST", "/api/v1/rules", `null`, 400},
		{"GET", "/api/v1/rules?limit=0", "", 400},
		{"GET", "/api/v1/rules?offset=-1", "", 400},
		{"GET", "/api/v1/rules?enabled=yes", "", 400},
		{"GET", "/api/v1/rules?limit=1&limit=2", "", 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 201 && w.Header().Get("Location") != "/api/v1/rules/example" {
				t.Fatal("missing Location")
			}
			if tc.method == "GET" && tc.status == 200 && strings.Contains(tc.path, "?") {
				var page struct {
					Items []ruleResponse `json:"items"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Items == nil {
					t.Fatalf("empty items must be []: %s", w.Body.String())
				}
			}
		})
	}
	if s.update.Enabled == nil || *s.update.Enabled || s.update.Salience == nil || *s.update.Salience != 0 || s.update.Content != nil {
		t.Fatalf("patch lost presence: %+v", s.update)
	}
	if s.create.Enabled == nil || *s.create.Enabled {
		t.Fatal("create lost false")
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{rulemanagement.ErrInvalid, 400}, {rulemanagement.ErrNotFound, 404}, {rulemanagement.ErrConflict, 409}, {errors.New("private database detail"), 500},
	} {
		h := NewServer(":0", &stubService{err: tc.err}, nil).Handler
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/rules/id", nil))
		if w.Code != tc.status {
			t.Fatalf("got %d", w.Code)
		}
		if tc.status == 500 && strings.Contains(w.Body.String(), "private") {
			t.Fatal("leaked database error")
		}
	}
}
