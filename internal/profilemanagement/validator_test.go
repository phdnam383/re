package profilemanagement

import (
	"errors"
	"re/internal/contextbuilder"
	"strings"
	"testing"
)

func TestValidator(t *testing.T) {
	valid := contextbuilder.ContextProfile{Name: "test", Selector: contextbuilder.Selector{AlertTypes: []string{"cpu"}}}
	for _, tc := range []struct {
		name   string
		change func(*contextbuilder.ContextProfile)
		valid  bool
	}{
		{"valid", func(p *contextbuilder.ContextProfile) {}, true},
		{"empty providers", func(p *contextbuilder.ContextProfile) { p.Providers = contextbuilder.ProviderSpec{} }, true},
		{"blank name", func(p *contextbuilder.ContextProfile) { p.Name = " " }, false},
		{"empty selector", func(p *contextbuilder.ContextProfile) { p.Selector = contextbuilder.Selector{} }, false},
		{"nested value", func(p *contextbuilder.ContextProfile) {
			p.Selector.AdditionalInformation = map[string][]any{"x": {map[string]any{"nested": true}}}
		}, false},
		{"nul", func(p *contextbuilder.ContextProfile) {
			p.Selector.AdditionalInformation = map[string][]any{"x": {"a\x00b"}}
		}, false},
		{"large", func(p *contextbuilder.ContextProfile) { p.Description = strings.Repeat("x", MaxProfileBytes) }, false},
		{"invalid link", func(p *contextbuilder.ContextProfile) { p.Providers.Link = []contextbuilder.LinkTarget{{Target: " "}} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			err := (Validator{}).Validate(Profile{ContextProfile: p})
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}
