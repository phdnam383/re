package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"re/internal/contextbuilder"
	cbpostgres "re/internal/contextbuilder/postgres"
	"re/internal/profilemanagement"
)

func TestRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "profile_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	if _, err = db.ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(data)
	start := strings.Index(ddl, "CREATE TABLE context_profile")
	end := strings.Index(ddl[start:], ");") + start + 2
	if _, err = db.ExecContext(ctx, ddl[start:end]); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	svc := profilemanagement.NewService(repo)
	loader := cbpostgres.NewProfileRepository(db)
	selector := contextbuilder.Selector{AlertTypes: []string{"cpu"}, AdditionalInformation: map[string][]any{"level": {"high", float64(2), true, nil}}}
	p, err := svc.Create(ctx, profilemanagement.CreateInput{Name: "example", Selector: selector, Providers: contextbuilder.ProviderSpec{VDU: []string{"ims.logic"}, Metric: []string{"cpu", "ram"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.ID == "" || p.CreatedAt.IsZero() {
		t.Fatal("bad defaults")
	}
	if _, err = svc.Create(ctx, profilemanagement.CreateInput{Name: "example", Selector: selector}); !errors.Is(err, profilemanagement.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	loaded, err := loader.LoadEnabled(ctx)
	if err != nil || len(loaded) != 1 || len(loaded[0].Providers.Metric) != 2 {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	old := p
	replacement := contextbuilder.ProviderSpec{Metric: []string{"new_cpu"}}
	newSelector := contextbuilder.Selector{ProbableCauses: []string{"overload"}}
	p, err = svc.Update(ctx, p.ID, profilemanagement.UpdateInput{Providers: &replacement, Selector: &newSelector})
	if err != nil {
		t.Fatal(err)
	}
	if !p.UpdatedAt.After(old.UpdatedAt) {
		t.Fatal("timestamp not advanced")
	}
	if _, err = repo.Update(ctx, old, old.UpdatedAt); !errors.Is(err, profilemanagement.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	loaded, err = loader.LoadEnabled(ctx)
	if err != nil || len(loaded) != 1 || len(loaded[0].Providers.VDU) != 0 || loaded[0].Providers.Metric[0] != "new_cpu" || len(loaded[0].Selector.AlertTypes) != 0 {
		t.Fatalf("replacement not visible: %+v %v", loaded, err)
	}
	empty := contextbuilder.Selector{}
	if _, err = svc.Update(ctx, p.ID, profilemanagement.UpdateInput{Selector: &empty}); !errors.Is(err, profilemanagement.ErrInvalid) {
		t.Fatalf("invalid selector: %v", err)
	}
	got, err := repo.Get(ctx, p.ID)
	if err != nil || got.Selector.ProbableCauses[0] != "overload" {
		t.Fatal("invalid change persisted")
	}
	disabled := false
	p, err = svc.Update(ctx, p.ID, profilemanagement.UpdateInput{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = loader.LoadEnabled(ctx)
	if err != nil || len(loaded) != 0 {
		t.Fatal("disabled profile loaded")
	}
	page, err := svc.List(ctx, profilemanagement.ListFilter{Enabled: &disabled, Limit: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}
	page, err = svc.List(ctx, profilemanagement.ListFilter{Limit: 10, Offset: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatal("bad past-end page")
	}
	enabled := true
	if _, err = svc.Update(ctx, p.ID, profilemanagement.UpdateInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = loader.LoadEnabled(ctx)
	if err != nil || len(loaded) != 0 {
		t.Fatal("deleted profile loaded")
	}
	if _, err = repo.Get(ctx, p.ID); !errors.Is(err, profilemanagement.ErrNotFound) {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, p.ID); !errors.Is(err, profilemanagement.ErrNotFound) {
		t.Fatal(err)
	}
}
