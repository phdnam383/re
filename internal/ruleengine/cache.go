package ruleengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"re/internal/analysis"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
)

type compiled struct {
	hash    string
	library *ast.KnowledgeLibrary

	ruleCount int
}

func (c *compiled) instance(ruleID string) (*ast.KnowledgeBase, error) {
	kb, err := c.library.NewKnowledgeBaseInstance(ruleID, c.hash)
	if err != nil {
		return nil, fmt.Errorf("rca_rule %s: instantiate: %w", ruleID, err)
	}
	return kb, nil
}

type ruleCache struct {
	mu    sync.Mutex
	items map[string]*cacheEntry
}

type cacheEntry struct {
	mu sync.Mutex
	c  *compiled
}

func newRuleCache() *ruleCache {
	return &ruleCache{items: map[string]*cacheEntry{}}
}

func (rc *ruleCache) get(rule analysis.RuleDefinition) (*compiled, error) {
	if strings.TrimSpace(rule.Content) == "" {
		return nil, fmt.Errorf("rca_rule %s (%s): rule_content is empty", rule.ID, rule.Name)
	}
	want := contentHash(rule.Content)

	rc.mu.Lock()
	entry, ok := rc.items[rule.ID]
	if !ok {
		entry = &cacheEntry{}
		rc.items[rule.ID] = entry
	}
	rc.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.c != nil && entry.c.hash == want {
		return entry.c, nil
	}

	c, err := compileRule(rule, want)
	if err != nil {

		entry.c = nil
		return nil, err
	}
	entry.c = c
	return c, nil
}

func compileRule(rule analysis.RuleDefinition, hash string) (*compiled, error) {
	library := ast.NewKnowledgeLibrary()
	b := builder.NewRuleBuilder(library)

	if err := b.BuildRuleFromResource(rule.ID, hash, pkg.NewBytesResource([]byte(rule.Content))); err != nil {
		return nil, fmt.Errorf("rca_rule %s (%s): compile: %w", rule.ID, rule.Name, err)
	}

	kb := library.GetKnowledgeBase(rule.ID, hash)
	if kb == nil || len(kb.RuleEntries) == 0 {

		return nil, fmt.Errorf("rca_rule %s (%s): compiled to no rules", rule.ID, rule.Name)
	}

	return &compiled{hash: hash, library: library, ruleCount: len(kb.RuleEntries)}, nil
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ValidateContent compiles GRL without executing it or populating the runtime cache.
func ValidateContent(content string) error {
	_, err := compileRule(analysis.RuleDefinition{ID: "validation", Name: "validation", Content: content}, contentHash(content))
	return err
}
