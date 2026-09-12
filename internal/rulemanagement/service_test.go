package rulemanagement

import (
	"context"
	"errors"
	"re/internal/analysis"
	"testing"
	"time"
)

const validGRL = `rule Example { when true then Retract("Example"); }`
const testID = "00000000-0000-0000-0000-000000000001"

type memoryRepo struct {
	rule     Rule
	writes   int
	conflict bool
}

func (m *memoryRepo) Create(_ context.Context, r Rule) (Rule, error) {
	m.writes++
	m.rule = r
	return r, nil
}
func (m *memoryRepo) Get(context.Context, string) (Rule, error)          { return m.rule, nil }
func (m *memoryRepo) List(context.Context, ListFilter) (RulePage, error) { return RulePage{}, nil }
func (m *memoryRepo) Delete(context.Context, string) error               { m.writes++; return nil }
func (m *memoryRepo) Update(_ context.Context, r Rule, expected time.Time) (Rule, error) {
	if m.conflict || !expected.Equal(m.rule.UpdatedAt) {
		return Rule{}, ErrConflict
	}
	m.writes++
	m.rule = r
	return r, nil
}

func TestCreateDefaultsAndRejectsInvalid(t *testing.T) {
	repo := &memoryRepo{}
	s := NewService(repo)
	r, err := s.Create(context.Background(), CreateInput{Name: " example ", Content: validGRL})
	if err != nil || !r.Enabled || r.Name != "example" {
		t.Fatalf("create: %+v %v", r, err)
	}
	_, err = s.Create(context.Background(), CreateInput{Name: "bad", Content: "not GRL"})
	if !errors.Is(err, ErrInvalid) || repo.writes != 1 {
		t.Fatalf("invalid rule persisted: %v writes=%d", err, repo.writes)
	}
}

func TestPatchPreservesOmittedFieldsAndRejectsInvalid(t *testing.T) {
	repo := &memoryRepo{rule: Rule{RuleDefinition: analysis.RuleDefinition{ID: testID, Name: "example", Content: validGRL, Description: "keep", Salience: 10, UpdatedAt: time.Now()}, Enabled: true}}
	s := NewService(repo)
	disabled, zero := false, 0
	r, err := s.Update(context.Background(), testID, UpdateInput{Enabled: &disabled, Salience: &zero})
	if err != nil || r.Enabled || r.Salience != 0 || r.Description != "keep" || r.Content != validGRL {
		t.Fatalf("patch: %+v %v", r, err)
	}
	bad := "invalid GRL"
	_, err = s.Update(context.Background(), testID, UpdateInput{Content: &bad})
	if !errors.Is(err, ErrInvalid) || repo.writes != 1 || repo.rule.Content != validGRL {
		t.Fatalf("invalid update changed rule: %v", err)
	}
	repo.conflict = true
	_, err = s.Update(context.Background(), testID, UpdateInput{Enabled: &disabled})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestInvalidRequests(t *testing.T) {
	s := NewService(&memoryRepo{})
	if _, err := s.Get(context.Background(), "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.Update(context.Background(), testID, UpdateInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, filter := range []ListFilter{{Limit: 101}, {Limit: -1}, {Offset: -1}} {
		if _, err := s.List(context.Background(), filter); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}
