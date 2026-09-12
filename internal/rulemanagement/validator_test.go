package rulemanagement

import (
	"errors"
	"re/internal/analysis"
	"strings"
	"testing"
)

func TestValidator(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"valid", validGRL, true}, {" ", validGRL, false}, {"empty", "", false},
		{"syntax", "rule broken {", false}, {"no rules", "// only a comment", false},
		{"large", strings.Repeat("a", MaxContentBytes+1), false}, {"nul\x00", validGRL, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (Validator{}).Validate(Rule{RuleDefinition: analysis.RuleDefinition{Name: tc.name, Content: tc.content}})
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}
