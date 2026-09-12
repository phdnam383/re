package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	enginepostgres "re/internal/ruleengine/postgres"
	"re/internal/rulemanagement"
)

// TEST_DATABASE_URL must point to a disposable database; each test uses its own schema.
func TestRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "rulemanagement_test_" + uuid.New().String()[0:8]
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	if _, err = db.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE rca_rule (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(), name VARCHAR NOT NULL UNIQUE, description TEXT,
 rule_content TEXT NOT NULL, salience INT NOT NULL DEFAULT 0, enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	service := rulemanagement.NewService(repo)
	const content = `rule Example { when true then Retract("Example"); }`
	r, err := service.Create(ctx, rulemanagement.CreateInput{Name: "example", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Enabled || r.ID == "" || r.CreatedAt.IsZero() {
		t.Fatalf("bad defaults: %+v", r)
	}
	if _, err = service.Create(ctx, rulemanagement.CreateInput{Name: "example", Content: content}); !errors.Is(err, rulemanagement.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	before := r
	disabled := false
	r, err = service.Update(ctx, r.ID, rulemanagement.UpdateInput{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if r.Enabled || !r.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("update: %+v", r)
	}
	if _, err = repo.Update(ctx, before, before.UpdatedAt); !errors.Is(err, rulemanagement.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	enabledRepo := enginepostgres.NewRuleRepository(db)
	loaded, err := enabledRepo.LoadEnabled(ctx)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("disabled rule loaded: %v %v", loaded, err)
	}
	page, err := service.List(ctx, rulemanagement.ListFilter{Enabled: &disabled, Limit: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list: %+v %v", page, err)
	}
	page, err = service.List(ctx, rulemanagement.ListFilter{Limit: 10, Offset: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatalf("past end: %+v %v", page, err)
	}
	enabled := true
	changed := `rule Changed { when true then Retract("Changed"); }`
	r, err = service.Update(ctx, r.ID, rulemanagement.UpdateInput{Enabled: &enabled, Content: &changed})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = enabledRepo.LoadEnabled(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Content != changed {
		t.Fatalf("new content not loaded: %v %v", loaded, err)
	}
	bad := "bad GRL"
	if _, err = service.Update(ctx, r.ID, rulemanagement.UpdateInput{Content: &bad}); !errors.Is(err, rulemanagement.ErrInvalid) {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, r.ID)
	if err != nil || got.Content != changed {
		t.Fatalf("invalid update persisted: %+v %v", got, err)
	}
	if err = service.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Get(ctx, r.ID); !errors.Is(err, rulemanagement.ErrNotFound) {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, r.ID); !errors.Is(err, rulemanagement.ErrNotFound) {
		t.Fatal(err)
	}
	loaded, err = enabledRepo.LoadEnabled(ctx)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("deleted rule loaded: %v %v", loaded, err)
	}
}

func TestMapError(t *testing.T) {
	if !errors.Is(mapError(sql.ErrNoRows), rulemanagement.ErrNotFound) {
		t.Fatal("missing not found mapping")
	}
}
