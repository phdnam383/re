package ruleengine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"re/internal/analysis"
)

// The rule text lives twice: as reviewable files under re/grule/ and as
// rca_rule.rule_content in db/seed_test.sql, which is what actually executes.
// Nothing keeps them in step, so this test does.
//
// A drift here is the worst kind of bug the rule set can have: the file a
// reviewer reads and approves is not the text the engine runs, and every other
// test in this package would still pass.

// seedRuleRe extracts (name, rule_content) from the INSERT INTO rca_rule
// statement, whose tuples read ('name', 'description', $grl$...$grl$, …).
//
// The content is dollar-quoted, which is what lets the GRL hold single quotes
// unescaped — and what makes splitting the seed on ';' unsafe, so each block is
// matched whole. The description is matched explicitly rather than skipped with
// a wildcard: the seed inserts other tables first, and a looser pattern reaches
// back past them and pairs the wrong name with the first rule body.
var seedRuleRe = regexp.MustCompile(`(?s)\('([a-zA-Z0-9_]+)',\s*'[^']*',\s*\$grl\$(.*?)\$grl\$`)

func TestShippedGRLFilesMatchTheSeededRuleContent(t *testing.T) {
	seedPath := filepath.Join("..", "..", "db", "seed_test.sql")
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read %s: %v", seedPath, err)
	}

	matches := seedRuleRe.FindAllStringSubmatch(string(seed), -1)
	if len(matches) == 0 {
		t.Fatal("no rca_rule rows found in the seed; the extraction pattern no longer matches")
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "grule", "*.grl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != len(files) {
		t.Errorf("seed has %d rca_rule rows but re/grule/ has %d files", len(matches), len(files))
	}

	seen := map[string]bool{}
	for _, m := range matches {
		name, content := m[1], m[2]
		seen[name] = true

		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "grule", name+".grl")
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("rca_rule %q has no matching file: %v", name, err)
			}

			wantNormalised := normaliseGRL(t, string(file))
			gotNormalised := normaliseGRL(t, content)
			if gotNormalised != wantNormalised {
				t.Errorf("rca_rule %q and grule/%s.grl differ beyond layout\n\nseed:\n%s\n\nfile:\n%s",
					name, name, gotNormalised, wantNormalised)
			}
		})
	}

	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".grl")
		if !seen[name] {
			t.Errorf("grule/%s.grl has no rca_rule row in the seed, so it never executes", name)
		}
	}
}

func TestShippedGRLFilesCompileAndDeclareRules(t *testing.T) {
	// A rule set that does not compile is caught at request time otherwise —
	// on the incident it was written for.
	files, err := filepath.Glob(filepath.Join("..", "..", "grule", "*.grl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no .grl files found")
	}

	cache := newRuleCache()
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".grl")
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := cache.get(analysis.RuleDefinition{ID: name, Name: name, Content: string(content)})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if c.ruleCount == 0 {
				t.Error("document declares no rules")
			}
		})
	}
}

// normaliseGRL strips every byte of whitespace that sits outside a string
// literal.
//
// GRL is not whitespace-sensitive there, and the two copies are laid out
// differently on purpose: the file is indented for review, the seed is
// flattened so a dollar-quoted SQL literal stays readable. Collapsing runs of
// whitespace to a single space is not enough — the file writes `IsDown(\n
// "a",\n "b"\n)` where the seed writes `IsDown("a", "b")`, which differ by
// spaces adjacent to punctuation.
//
// Literals are preserved byte for byte, because a space inside an LTREE path
// or a summary is content.
func normaliseGRL(t *testing.T, s string) string {
	t.Helper()

	// The stripper would splice a `//` comment onto the line below it and
	// produce a false match. Neither copy has comments today; this fails
	// loudly rather than silently comparing corrupted text if one gains them.
	if strings.Contains(s, "//") || strings.Contains(s, "/*") {
		t.Fatal("normaliseGRL cannot handle comments; teach it before adding any to a rule")
	}

	var b strings.Builder
	b.Grow(len(s))

	inString := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case inString && r == '\\':
			b.WriteRune(r)
			escaped = true
		case r == '"':
			inString = !inString
			b.WriteRune(r)
		case inString:
			b.WriteRune(r)
		case unicode.IsSpace(r):
			// Layout, not content.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestNormaliseGRLKeepsLiteralsIntact(t *testing.T) {
	got := normaliseGRL(t, "rule A {\n when\tCtx.X(\"a b\",  \"c\") then Y();\n}")
	want := `ruleA{whenCtx.X("a b","c")thenY();}`
	if got != want {
		t.Errorf("normaliseGRL = %q, want %q", got, want)
	}
}
