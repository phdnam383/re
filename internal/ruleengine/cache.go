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

// compiled is one rule document parsed into an executable form.
//
// library holds the blueprint AST. It is never executed directly: working
// memory is mutable, so each run takes a clone, and two concurrent analyses
// sharing one blueprint would corrupt each other's evaluation state.
type compiled struct {
	hash    string
	library *ast.KnowledgeLibrary

	// ruleCount is how many GRL rules the document declares. It becomes the
	// engine's cycle bound, which is why it is captured at compile time rather
	// than guessed.
	ruleCount int
}

// instance clones the blueprint into a knowledge base that is safe to execute.
func (c *compiled) instance(ruleID string) (*ast.KnowledgeBase, error) {
	kb, err := c.library.NewKnowledgeBaseInstance(ruleID, c.hash)
	if err != nil {
		return nil, fmt.Errorf("rca_rule %s: instantiate: %w", ruleID, err)
	}
	return kb, nil
}

// ruleCache compiles rule documents once and hands out clones.
//
// Compiling GRL is expensive and must never happen on the request path more
// than once per distinct content. The key is the rule id plus a digest of its
// content, not the id alone: rca_rule rows are edited in place, and keying on
// the id would keep executing the previous text forever after an operator
// changed it — the one failure mode that is invisible from the outside,
// because the engine keeps answering, just from stale rules.
//
// Each rule id owns its own KnowledgeLibrary, and a content change replaces
// that library wholesale. Grule's library is a map keyed by name and version
// with no eviction, so compiling every edit into one shared library would
// accumulate every version a long-lived process ever saw.
type ruleCache struct {
	mu    sync.Mutex
	items map[string]*cacheEntry
}

// cacheEntry has its own lock so a slow compile blocks only the rule being
// compiled. The cache lock is held just long enough to find the entry.
type cacheEntry struct {
	mu sync.Mutex
	c  *compiled
}

func newRuleCache() *ruleCache {
	return &ruleCache{items: map[string]*cacheEntry{}}
}

// get returns the compiled form of a rule, compiling it if the cache does not
// already hold this exact content.
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
		// Drop whatever was cached. Keeping the previous compilation would
		// mean a row whose new content does not compile silently keeps
		// answering with the old rules — the engine would look healthy while
		// running text the operator has already replaced.
		entry.c = nil
		return nil, err
	}
	entry.c = c
	return c, nil
}

// compileRule parses one document into its own library.
func compileRule(rule analysis.RuleDefinition, hash string) (*compiled, error) {
	library := ast.NewKnowledgeLibrary()
	b := builder.NewRuleBuilder(library)

	if err := b.BuildRuleFromResource(rule.ID, hash, pkg.NewBytesResource([]byte(rule.Content))); err != nil {
		return nil, fmt.Errorf("rca_rule %s (%s): compile: %w", rule.ID, rule.Name, err)
	}

	kb := library.GetKnowledgeBase(rule.ID, hash)
	if kb == nil || len(kb.RuleEntries) == 0 {
		// Syntactically valid and behaviourally empty — a comment-only
		// document, most often the result of an edit that deleted the rule
		// body. It would otherwise run, match nothing and be reported as a
		// rule that had nothing to say.
		return nil, fmt.Errorf("rca_rule %s (%s): compiled to no rules", rule.ID, rule.Name)
	}

	return &compiled{hash: hash, library: library, ruleCount: len(kb.RuleEntries)}, nil
}

// contentHash content-addresses a rule document.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
