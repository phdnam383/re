// Integration tests for the PostgreSQL providers.
//
// They need a reachable PostgreSQL and skip without one:
//
//	$env:RE_TEST_DB_DSN = 'postgres://user:pass@localhost:5432/re_test?sslmode=disable'
//	go test ./internal/contextbuilder/postgres/
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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"re/internal/analysis"
	"re/internal/contextbuilder"
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

func TestProfileRepositoryLoadEnabled(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	profiles, err := NewProfileRepository(db).LoadEnabled(ctx)
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}

	want := []string{"link_to_peer_diagw_down_0001", "link_to_peer_sipgw_down_0001", "tps_overloaded_0001"}
	if len(profiles) != len(want) {
		t.Fatalf("loaded %d profiles, want %d", len(profiles), len(want))
	}
	for i, name := range want {
		if profiles[i].Name != name {
			t.Errorf("profile[%d] = %q, want %q (rows must arrive in name order)", i, profiles[i].Name, name)
		}
	}

	tps := profiles[2]
	if got := tps.Selector.AdditionalInformation["metric"]; len(got) != 1 || got[0] != "overload_ram" {
		t.Errorf("tps selector metric = %v", got)
	}
	if len(tps.Providers.Configuration) != 1 || tps.Providers.Configuration[0].Key != "number_of_log_file" {
		t.Errorf("tps configuration = %+v", tps.Providers.Configuration)
	}

	sipgw := profiles[1]
	if len(sipgw.Providers.VDU) != 4 || len(sipgw.Providers.Link) != 1 {
		t.Errorf("sipgw providers = %+v", sipgw.Providers)
	}
}

func TestProfileRepositorySkipsDisabled(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		"UPDATE context_profile SET enabled = FALSE WHERE name = 'tps_overloaded_0001'"); err != nil {
		t.Fatalf("disable profile: %v", err)
	}

	profiles, err := NewProfileRepository(db).LoadEnabled(ctx)
	if err != nil {
		t.Fatalf("LoadEnabled() error = %v", err)
	}
	for _, p := range profiles {
		if p.Name == "tps_overloaded_0001" {
			t.Fatal("a disabled profile was loaded")
		}
	}
	if len(profiles) != 2 {
		t.Errorf("loaded %d profiles, want 2", len(profiles))
	}
}

func TestVDUProviderReturnsSubtree(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	res, err := NewVDUProvider(db).FetchVDUs(ctx, []string{"ims.vdu_sb_logic"})
	if err != nil {
		t.Fatalf("FetchVDUs() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("missing = %+v", res.Missing)
	}
	if len(res.VDUs) != 1 {
		t.Fatalf("vdus = %d, want 1", len(res.VDUs))
	}

	vdu := res.VDUs[0]
	if vdu.Name != "sb_logic" || vdu.Type != "LOGIC" || vdu.Replicas != 3 {
		t.Errorf("vdu = %+v", vdu)
	}
	if vdu.Namespace != "ims" || vdu.Workload != "StatefulSet" || vdu.Selector != "app=sb-logic" {
		t.Errorf("vdu columns not fully returned: %+v", vdu)
	}
	if vdu.UpdatedAt.IsZero() {
		t.Error("updated_at is zero")
	}

	// The ltree descendant query must find every VNFC under the VDU, and each
	// one must carry the VDU it was found under — vnfc has no vdu_path column.
	if len(res.VNFCs) != 3 {
		t.Fatalf("vnfcs = %d, want 3", len(res.VNFCs))
	}
	for _, vnfc := range res.VNFCs {
		if vnfc.VDUPath != "ims.vdu_sb_logic" {
			t.Errorf("%s vdu_path = %q", vnfc.Path, vnfc.VDUPath)
		}
	}

	first := res.VNFCs[0]
	if first.Path != "ims.vdu_sb_logic.vnfc_sb_logic_1" || first.Status != "RUNNING" {
		t.Errorf("vnfc[0] = %+v", first)
	}
	if first.K8sUID == "" || first.Name != "sb-logic-1" {
		t.Errorf("vnfc columns not fully returned: %+v", first)
	}
}

func TestVDUProviderDecodesJSONBColumns(t *testing.T) {
	db := testDB(t)

	res, err := NewVDUProvider(db).FetchVDUs(context.Background(), []string{"ims.vdu_sb_sip_core"})
	if err != nil {
		t.Fatalf("FetchVDUs() error = %v", err)
	}
	if got := res.VDUs[0].NFConfig["sip_port"]; got != float64(5060) {
		t.Errorf("nf_config.sip_port = %#v, want 5060", got)
	}

	var found bool
	for _, vnfc := range res.VNFCs {
		if vnfc.Path == "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1" {
			found = true
			if got := vnfc.InstanceConfig["zone"]; got != "zone-a" {
				t.Errorf("instance_config.zone = %#v", got)
			}
		}
	}
	if !found {
		t.Error("vnfc_sb_sip_core_1 not returned")
	}
}

// A NULL jsonb column must decode to a nil map rather than failing the query.
func TestVDUProviderHandlesNullJSONB(t *testing.T) {
	db := testDB(t)

	res, err := NewVDUProvider(db).FetchVDUs(context.Background(), []string{"ims.vdu_sb_logic"})
	if err != nil {
		t.Fatalf("FetchVDUs() error = %v", err)
	}
	if res.VDUs[0].NFConfig != nil {
		t.Errorf("nf_config = %#v, the seeded row is NULL", res.VDUs[0].NFConfig)
	}
}

// A VDU with no VNFC is a result, not a gap: a workload scaled to zero is a
// fact a rule may want to conclude from.
func TestVDUProviderVDUWithoutVNFCIsNotMissing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO managed_object (path, mo_class) VALUES ('ims.vdu_empty', 'VDU');
		INSERT INTO vdu (path, name, type, namespace, workload, replicas)
		VALUES ('ims.vdu_empty', 'empty', 'LOGIC', 'ims', 'Deployment', 0)`); err != nil {
		t.Fatalf("insert empty vdu: %v", err)
	}

	res, err := NewVDUProvider(db).FetchVDUs(ctx, []string{"ims.vdu_empty"})
	if err != nil {
		t.Fatalf("FetchVDUs() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Errorf("missing = %+v, an empty VDU is not a gap", res.Missing)
	}
	if len(res.VDUs) != 1 || len(res.VNFCs) != 0 {
		t.Errorf("vdus = %d, vnfcs = %d", len(res.VDUs), len(res.VNFCs))
	}
}

func TestVDUProviderReportsUnknownPaths(t *testing.T) {
	db := testDB(t)

	res, err := NewVDUProvider(db).FetchVDUs(context.Background(),
		[]string{"ims.vdu_sb_logic", "ims.vdu_does_not_exist"})
	if err != nil {
		t.Fatalf("FetchVDUs() error = %v", err)
	}
	if len(res.VDUs) != 1 {
		t.Errorf("vdus = %d, the resolved path must survive", len(res.VDUs))
	}
	want := []analysis.MissingContext{{
		Provider: analysis.ProviderVDU,
		Entity:   "ims.vdu_does_not_exist",
		Reason:   analysis.ReasonNotFound,
	}}
	if len(res.Missing) != 1 || res.Missing[0] != want[0] {
		t.Errorf("missing = %+v, want %+v", res.Missing, want)
	}
}

func TestLinkProviderExactDirectedPair(t *testing.T) {
	db := testDB(t)

	forward := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
		DstPath: "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
	}

	res, err := NewLinkProvider(db).FetchLinks(context.Background(), []contextbuilder.LinkTarget{forward})
	if err != nil {
		t.Fatalf("FetchLinks() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("missing = %+v", res.Missing)
	}

	// Exactly the requested direction. The seed also holds the reverse pair, so
	// a provider that walked or normalised would return two rows here.
	if len(res.Links) != 1 {
		t.Fatalf("links = %d, want exactly the requested pair: %+v", len(res.Links), res.Links)
	}
	link := res.Links[0]
	if link.SrcPath != forward.SrcPath || link.DstPath != forward.DstPath {
		t.Errorf("link = %s -> %s", link.SrcPath, link.DstPath)
	}
	if link.Protocol != "SIP" || link.Status != "DOWN" {
		t.Errorf("link columns = %+v", link)
	}
	if link.CreatedAt.IsZero() || link.UpdatedAt.IsZero() {
		t.Error("timestamps are zero")
	}
}

func TestLinkProviderVDUPairCoversEveryInstance(t *testing.T) {
	// The reason a profile names VDUs: sb_sip_core runs two instances and both
	// have an edge to the load balancer. An instance-pair target fetched one of
	// them, and would have kept fetching one however far the VDU scaled.
	db := testDB(t)

	target := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_sb_sip_core",
		DstPath: "ims.vdu_cs_loadbalancer_icscf",
	}

	res, err := NewLinkProvider(db).FetchLinks(context.Background(), []contextbuilder.LinkTarget{target})
	if err != nil {
		t.Fatalf("FetchLinks() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("missing = %+v", res.Missing)
	}
	if len(res.Links) != 2 {
		t.Fatalf("links = %d, want both instances' edges: %+v", len(res.Links), res.Links)
	}
	for _, l := range res.Links {
		if !target.Matches(l.SrcPath, l.DstPath) {
			t.Errorf("link %s -> %s is outside the target", l.SrcPath, l.DstPath)
		}
	}
	// Still one direction only. The seed holds the reverse edges too, and a
	// subtree match on both endpoints must not start returning them.
	if res.Links[0].SrcPath == res.Links[1].SrcPath {
		t.Errorf("links = %+v, want one per source instance", res.Links)
	}
}

func TestLinkProviderOverlappingTargetsYieldOneRowEach(t *testing.T) {
	// Two targets can select the same edge. A duplicated row would make one
	// link count for two in anything that counts them, which is what the
	// quantified link facts in the rule engine do.
	db := testDB(t)

	broad := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_sb_sip_core",
		DstPath: "ims.vdu_cs_loadbalancer_icscf",
	}
	narrow := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
		DstPath: "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
	}

	res, err := NewLinkProvider(db).FetchLinks(context.Background(),
		[]contextbuilder.LinkTarget{broad, narrow})
	if err != nil {
		t.Fatalf("FetchLinks() error = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("missing = %+v, both targets are satisfied", res.Missing)
	}
	if len(res.Links) != 2 {
		t.Errorf("links = %d, want 2 distinct rows: %+v", len(res.Links), res.Links)
	}
}

func TestLinkProviderReportsUnknownPairs(t *testing.T) {
	db := testDB(t)

	present := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
		DstPath: "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
	}
	// Both endpoints exist and are linked in the other direction only, which is
	// the case a traversal-based provider would silently paper over.
	absent := contextbuilder.LinkTarget{
		SrcPath: "ims.vdu_cs_logic.vnfc_cs_logic_1",
		DstPath: "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
	}

	res, err := NewLinkProvider(db).FetchLinks(context.Background(),
		[]contextbuilder.LinkTarget{present, absent})
	if err != nil {
		t.Fatalf("FetchLinks() error = %v", err)
	}
	if len(res.Links) != 1 {
		t.Errorf("links = %d, want 1", len(res.Links))
	}
	if len(res.Missing) != 1 {
		t.Fatalf("missing = %+v, want 1", res.Missing)
	}
	if res.Missing[0].Entity != absent.Entity() || res.Missing[0].Reason != analysis.ReasonNotFound {
		t.Errorf("missing = %+v", res.Missing[0])
	}
}

func TestLinkProviderNoTargets(t *testing.T) {
	db := testDB(t)

	res, err := NewLinkProvider(db).FetchLinks(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchLinks() error = %v", err)
	}
	if len(res.Links) != 0 || len(res.Missing) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
}
