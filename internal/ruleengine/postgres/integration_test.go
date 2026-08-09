// Integration tests for the RCA rule repository.
//
// They need a reachable PostgreSQL and skip without one:
//
//	$env:RE_TEST_DB_DSN = 'postgres://user:pass@localhost:5432/re_test?sslmode=disable'
//	go test ./internal/ruleengine/postgres/
//
// Each run creates its own schema, applies db/schema.sql and db/seed_test.sql
// into it, and drops it on cleanup. Nothing outside that schema is touched, so
// the tests are safe against a database that already holds real data — and two
// runs cannot collide.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"re/internal/analysis"
	"re/internal/ruleengine"
)

const dsnEnv = "RE_TEST_DB_DSN"

// testDB provisions an isolated schema seeded from db/.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL integration tests", dsnEnv)
	}

	schema := fmt.Sprintf("re_test_%d", time.Now().UnixNano())

	// Extensions go into public first, before the test schema exists.
	//
	// CREATE EXTENSION installs into the first schema on the search path, so
	// applying db/schema.sql with the test schema in front puts ltree inside
	// it — and DROP SCHEMA CASCADE then takes the extension away with it.
	// pg_extension is database-wide, so the IF NOT EXISTS in db/schema.sql
	// would afterwards be satisfied for every other test schema while the
	// ltree type stayed unreachable from their search paths.
	ensureExtensions(t, dsn)

	// public stays on the search path so those extensions resolve; the test
	// schema comes first so every table db/schema.sql creates lands inside it.
	searchPath := schema + ",public"

	// The setup handle speaks the simple protocol because both .sql files hold
	// many statements in one document, which the extended protocol refuses.
	setup := openDB(t, dsn, searchPath, true)
	defer setup.Close()

	ctx := context.Background()
	if _, err := setup.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	db := openDB(t, dsn, searchPath, false)
	t.Cleanup(func() {
		db.Close()
		drop := openDB(t, dsn, "public", true)
		defer drop.Close()
		if _, err := drop.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	for _, file := range []string{"schema.sql", "seed_test.sql"} {
		path := filepath.Join("..", "..", "..", "db", file)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := setup.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}

	return db
}

func openDB(t *testing.T, dsn, searchPath string, simpleProtocol bool) *sql.DB {
	t.Helper()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", dsnEnv, err)
	}
	// Set as a runtime parameter rather than with SET, which would apply to
	// one pooled connection only.
	cfg.RuntimeParams["search_path"] = searchPath
	if simpleProtocol {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}

	db := stdlib.OpenDB(*cfg)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatalf("connect: %v", err)
	}
	return db
}

// ensureExtensions installs db/schema.sql's extensions into public.
//
// SCHEMA public is stated rather than left to the search path, so where they
// land does not depend on which handle happens to run this.
//
// Tolerating a concurrent creation is required, not defensive: Go runs each
// package's tests in its own process and those processes run in parallel, so
// two of them race on the same CREATE EXTENSION against one database. The
// loser gets a duplicate key on pg_extension, which means the extension is
// there — which is all this function wanted.
func ensureExtensions(t *testing.T, dsn string) {
	t.Helper()

	db := openDB(t, dsn, "public", true)
	defer db.Close()

	for _, name := range []string{"pgcrypto", "btree_gin", "ltree"} {
		_, err := db.ExecContext(context.Background(),
			fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %q SCHEMA public", name))
		if err == nil || alreadyExists(err) {
			continue
		}
		t.Fatalf("create extension %s: %v", name, err)
	}
}

// alreadyExists reports whether an error is PostgreSQL saying the object is
// already there: 23505 unique_violation on pg_extension, or 42710
// duplicate_object.
func alreadyExists(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" || pgErr.Code == "42710"
}

// emptySnapshot carries no topology, so no shipped rule can match. It is what
// the compile check runs against: the point is that the stored text parses and
// evaluates, not that it concludes anything.
func emptySnapshot() analysis.ContextSnapshot {
	return analysis.ContextSnapshot{Status: analysis.StatusComplete}
}

func TestRuleRepositoryLoadEnabled(t *testing.T) {
	db := testDB(t)

	rules, err := NewRuleRepository(db).LoadEnabled(context.Background())
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}

	// All three seeded rules share salience 100, so the name tie-break decides
	// the order — which is the point of having one.
	want := []string{"link_to_diagw_down", "link_to_sipgw_down", "tps_overloaded"}
	if len(rules) != len(want) {
		t.Fatalf("loaded %d rules, want %d", len(rules), len(want))
	}
	for i, name := range want {
		if rules[i].Name != name {
			t.Errorf("rule[%d] = %q, want %q", i, rules[i].Name, name)
		}
	}

	for _, r := range rules {
		if r.ID == "" {
			t.Errorf("rule %q has no id", r.Name)
		}
		if strings.TrimSpace(r.Content) == "" {
			t.Errorf("rule %q has empty content", r.Name)
		}
		if r.Description == "" {
			t.Errorf("rule %q has no description", r.Name)
		}
		if r.UpdatedAt.IsZero() {
			t.Errorf("rule %q has no updated_at", r.Name)
		}
		if r.UpdatedAt.Location() != time.UTC {
			t.Errorf("rule %q updated_at is %v, want UTC", r.Name, r.UpdatedAt.Location())
		}
	}
}

func TestRuleRepositoryOrdersBySalienceThenName(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Raise one rule and lower another so salience, not the name tie-break, is
	// what the order is being read from.
	if _, err := db.ExecContext(ctx,
		"UPDATE rca_rule SET salience = 200 WHERE name = 'tps_overloaded'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE rca_rule SET salience = 10 WHERE name = 'link_to_diagw_down'"); err != nil {
		t.Fatal(err)
	}

	rules, err := NewRuleRepository(db).LoadEnabled(ctx)
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}

	want := []string{"tps_overloaded", "link_to_sipgw_down", "link_to_diagw_down"}
	if len(rules) != len(want) {
		t.Fatalf("loaded %d rules, want %d", len(rules), len(want))
	}
	for i, name := range want {
		if rules[i].Name != name {
			t.Errorf("rule[%d] = %q, want %q", i, rules[i].Name, name)
		}
	}
	if rules[0].Salience != 200 {
		t.Errorf("salience = %d, want 200", rules[0].Salience)
	}
}

func TestRuleRepositorySkipsDisabledRules(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		"UPDATE rca_rule SET enabled = FALSE WHERE name = 'tps_overloaded'"); err != nil {
		t.Fatal(err)
	}

	rules, err := NewRuleRepository(db).LoadEnabled(ctx)
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}
	for _, r := range rules {
		if r.Name == "tps_overloaded" {
			t.Fatal("a disabled rule was loaded")
		}
	}
	if len(rules) != 2 {
		t.Errorf("loaded %d rules, want 2", len(rules))
	}
}

func TestRuleRepositoryReturnsNothingWhenAllRulesAreDisabled(t *testing.T) {
	// The repository reports an empty set, not an error. Turning that into
	// ErrRCARuleNotFound is the engine's decision, so the two stay separable.
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "UPDATE rca_rule SET enabled = FALSE"); err != nil {
		t.Fatal(err)
	}

	rules, err := NewRuleRepository(db).LoadEnabled(ctx)
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("loaded %d rules, want 0", len(rules))
	}
}

func TestRuleRepositoryLoadsContentThatCompiles(t *testing.T) {
	// The seeded rule_content is what actually executes in production. That it
	// survives the round trip through a dollar-quoted SQL literal is only
	// provable against a real database.
	db := testDB(t)

	rules, err := NewRuleRepository(db).LoadEnabled(context.Background())
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}

	runtime := ruleengine.NewGRLRuntime()
	for _, r := range rules {
		t.Run(r.Name, func(t *testing.T) {
			// An empty snapshot: no rule can match, so this exercises compile
			// and evaluation without depending on any fixture topology.
			err := runtime.Execute(context.Background(), r, ruleengine.NewFacts(emptySnapshot()), ruleengine.NewResult())
			if err != nil {
				t.Errorf("execute: %v", err)
			}
		})
	}
}

func TestRuleRepositoryFailsOnAClosedHandle(t *testing.T) {
	// A query failure has to reach the caller: analysing an incident against
	// an unknown subset of the operator's rules and reporting the outcome as
	// an answer is worse than reporting that the rules could not be read.
	db := testDB(t)
	db.Close()

	if _, err := NewRuleRepository(db).LoadEnabled(context.Background()); err == nil {
		t.Fatal("LoadEnabled() = nil, want an error")
	}
}
