// End-to-end tests for the whole engine.
//
// They drive the generated gRPC client against the real composition root:
//
//	protobuf request
//	  → gRPC transport
//	  → Analysis Service
//	  → PostgreSQL Context Builder
//	  → HTTP configuration stub
//	  → PostgreSQL rule repository + GRL runtime
//	  → protobuf response
//
// buildServer is called rather than re-assembled here, so a wiring mistake in
// the shipping binary fails these tests instead of hiding behind a second,
// agreeing copy of the wiring.
//
// They need a reachable PostgreSQL and skip without one:
//
//	$env:RE_TEST_DB_DSN = 'postgres://user:pass@localhost:5432/re_test?sslmode=disable'
//	go test ./cmd/engine/
//
// Each run creates its own schema, applies db/schema.sql and db/seed_test.sql
// into it, and drops it on cleanup.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"re/gen/mdafv1"
	"re/internal/analysis"
)

const dsnEnv = "RE_TEST_DB_DSN"

// --- database ------------------------------------------------------------

func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the end-to-end tests", dsnEnv)
	}

	schema := fmt.Sprintf("re_e2e_%d", time.Now().UnixNano())

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
	setup := openTestDB(t, dsn, searchPath, true)
	defer setup.Close()

	ctx := context.Background()
	if _, err := setup.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	db := openTestDB(t, dsn, searchPath, false)
	t.Cleanup(func() {
		db.Close()
		drop := openTestDB(t, dsn, "public", true)
		defer drop.Close()
		if _, err := drop.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	for _, file := range []string{"schema.sql", "seed_test.sql"} {
		path := filepath.Join("..", "..", "db", file)
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

func openTestDB(t *testing.T, dsn, searchPath string, simpleProtocol bool) *sql.DB {
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

	db := openTestDB(t, dsn, "public", true)
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

// --- configuration stub --------------------------------------------------

// startConfigurationAPI serves the effective configuration the TPS scenario
// needs and repoints the seeded profile at it.
//
// db/seed_test.sql stores an unreachable production URL on purpose: the
// effective value is never in PostgreSQL, and a seed that could be satisfied
// from the database would hide exactly the mistake the Configuration Provider
// exists to prevent. The test rewrites the URL rather than the value.
func startConfigurationAPI(t *testing.T, db *sql.DB, logFileCount any) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/num_of_log_file") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(logFileCount); err != nil {
			t.Errorf("encode configuration response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	repointConfigurationURL(t, db, server.URL+"/v1/ims.vdu_sb_logic.vnfc_sb_logic_1/num_of_log_file")
	return server
}

func repointConfigurationURL(t *testing.T, db *sql.DB, url string) {
	t.Helper()

	providers := fmt.Sprintf(`{"vdu":["ims.vdu_sb_logic"],"configuration":[{"path":"ims.vdu_sb_logic.vnfc_sb_logic_1","key":"number_of_log_file","url":%q}]}`, url)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE context_profile SET providers = $1::jsonb WHERE name = 'tps_overloaded_0001'", providers,
	); err != nil {
		t.Fatalf("repoint configuration url: %v", err)
	}
}

// --- engine harness ------------------------------------------------------

// startEngine wires the shipping composition root over an in-memory listener.
func startEngine(t *testing.T, db *sql.DB) mdafv1.IncidentAnalysisEngineClient {
	t.Helper()

	cfg := config{
		DSN:                  "unused",
		GRPCAddr:             "bufnet",
		ConfigurationTimeout: 2 * time.Second,
		RCARuleTimeout:       800 * time.Millisecond,
	}

	server, err := buildServer(cfg, db, testLogger())
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, testLogger(), server, listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	return mdafv1.NewIncidentAnalysisEngineClient(conn)
}

// --- requests ------------------------------------------------------------

// The alerts mirror db/seed_test.sql. They are written out rather than read
// back from the alert table because the engine takes them from the caller: the
// request is the input, and the database is only consulted for context.
func sipgwRequest() *mdafv1.AnalyzeIncidentRequest {
	return &mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-sipgw-0001",
		Incident:  "inc-sipgw-0001",
		Alerts: []*mdafv1.Alert{
			alert("aaaaaaaa-1111-4111-8111-111111111111", "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
				"LINK_TO_PEER_SIPGW_DOWN", "2026-06-18T00:00:00Z",
				map[string]any{
					"dst_path":    "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
					"remote_ip":   "10.55.70.37",
					"remote_port": 5060,
					"transport":   "TCP",
				}),
			alert("aaaaaaaa-2222-4222-8222-222222222222", "ims.vdu_sb_sip_core.vnfc_sb_sip_core_2",
				"LINK_TO_PEER_SIPGW_DOWN", "2026-06-18T00:00:01Z",
				map[string]any{
					"dst_path":    "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
					"remote_ip":   "10.55.70.37",
					"remote_port": 5060,
					"transport":   "TCP",
				}),
		},
	}
}

func diagwRequest() *mdafv1.AnalyzeIncidentRequest {
	info := map[string]any{
		"dst_path":             "ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1",
		"application_protocol": "DIAMETER",
		"transport":            "SCTP",
	}
	return &mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-diagw-0001",
		Incident:  "inc-diagw-0001",
		Alerts: []*mdafv1.Alert{
			alert("eeeeeeee-1111-4111-8111-111111111111", "ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1",
				"LINK_TO_PEER_DIAGW_DOWN", "2026-06-18T01:00:00Z", info),
			alert("eeeeeeee-2222-4222-8222-222222222222", "ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2",
				"LINK_TO_PEER_DIAGW_DOWN", "2026-06-18T01:00:01Z", info),
		},
	}
}

func tpsRequest() *mdafv1.AnalyzeIncidentRequest {
	req := &mdafv1.AnalyzeIncidentRequest{
		RequestId: "req-tps-0001",
		Incident:  "inc-tps-0001",
	}
	for i, observed := range []float64{93.5, 94.1, 95.0} {
		a := alert(
			fmt.Sprintf("cccccccc-3333-4333-8333-33333333333%d", i+3),
			"ims.vdu_sb_logic.vnfc_sb_logic_1",
			"THRESHOLD_CROSSING",
			fmt.Sprintf("2026-06-18T10:00:0%dZ", i*2),
			map[string]any{
				"metric":          "overload_ram",
				"observed_value":  observed,
				"threshold_value": 85,
			},
		)
		a.AlertType = "QUALITY_OF_SERVICE_ALERT"
		req.Alerts = append(req.Alerts, a)
	}
	return req
}

func alert(id, source, cause, createdAt string, info map[string]any) *mdafv1.Alert {
	a := &mdafv1.Alert{
		Id:                id,
		SourcePath:        source,
		AlertType:         "COMMUNICATIONS_ALERT",
		ProbableCause:     cause,
		PerceivedSeverity: "CRITICAL",
		State:             "ACTIVE",
		CreatedAt:         createdAt,
	}
	if info != nil {
		s, err := structpb.NewStruct(info)
		if err != nil {
			panic(err)
		}
		a.AdditionalInformation = s
	}
	return a
}

// --- golden scenarios ----------------------------------------------------

func TestE2EGoldenScenarios(t *testing.T) {
	tests := []struct {
		name string
		req  func() *mdafv1.AnalyzeIncidentRequest
	}{
		{name: "sipgw_link_down", req: sipgwRequest},
		{name: "diagw_link_down", req: diagwRequest},
		{name: "tps_overloaded", req: tpsRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			// The stub answers 5, which is what db/seed_test.sql documents as
			// the fixture and what makes the log-file rule fire.
			startConfigurationAPI(t, db, 5)
			client := startEngine(t, db)

			got, err := client.AnalyzeIncident(context.Background(), tc.req())
			if err != nil {
				t.Fatalf("AnalyzeIncident: %v", err)
			}
			assertGoldenResponse(t, tc.name, got)
		})
	}
}

func TestE2ESIPGWNamesEveryTerminatedComponent(t *testing.T) {
	db := testDB(t)
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), sipgwRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}

	if got.GetStatus().GetOverall() != analysis.RCAStatusComplete {
		t.Errorf("overall = %q, want COMPLETE", got.GetStatus().GetOverall())
	}
	// One id per blamed instance, and the order is the snapshot's VNFC order
	// rather than the rules' salience: the document fans out over the instances
	// the topology actually has, so the instances decide the order.
	want := []string{
		"rc-sipgw-loadbalancer-down-ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1",
		"rc-sipgw-logic-down-ims.vdu_cs_logic.vnfc_cs_logic_1",
		"rc-sipgw-icscf-down-ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1",
	}
	if got := rootCauseIDs(got); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("root causes = %v, want %v", got, want)
	}
}

func TestE2ETPSReadsTheEffectiveConfiguration(t *testing.T) {
	// The value comes from the HTTP API and nowhere else. This is the seam the
	// Configuration Provider exists for: PostgreSQL holds what was declared,
	// and only the NF knows what it is running.
	db := testDB(t)
	startConfigurationAPI(t, db, 5)
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), tpsRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}

	if got.GetStatus().GetOverall() != analysis.RCAStatusComplete {
		t.Errorf("overall = %q, want COMPLETE", got.GetStatus().GetOverall())
	}
	// The replica finding is about the VDU, so its rule names no subject and
	// keeps a fixed id. The configuration finding is about one instance and
	// carries that instance's path.
	want := []string{
		"rc-tps-replica-degradation",
		"rc-tps-high-log-file-config-ims.vdu_sb_logic.vnfc_sb_logic_1",
	}
	if ids := rootCauseIDs(got); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("root causes = %v, want %v", ids, want)
	}

	// The replica action carries a number through structpb, not a string.
	restore := got.GetRca().GetRootCauses()[0].GetActions()[0]
	if restore.GetValue().GetNumberValue() != 3 {
		t.Errorf("restore value = %v, want 3", restore.GetValue())
	}
}

func TestE2ETPSBelowThresholdBlamesOnlyTheReplicas(t *testing.T) {
	// The configuration rule fires at >= 3. Answering 1 leaves the replica
	// degradation standing on its own, which proves the value is really being
	// read rather than assumed.
	db := testDB(t)
	startConfigurationAPI(t, db, 1)
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), tpsRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	want := []string{"rc-tps-replica-degradation"}
	if ids := rootCauseIDs(got); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("root causes = %v, want %v", ids, want)
	}
}

// --- failure scenarios ---------------------------------------------------

func TestE2EPartialContextWhenTheConfigurationAPIIsDown(t *testing.T) {
	db := testDB(t)
	// Point at a port that refuses immediately: the GET fails, the snapshot
	// degrades and the analysis has to keep going.
	repointConfigurationURL(t, db, "http://127.0.0.1:1/v1/num_of_log_file")
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), tpsRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident = %v, want a successful PARTIAL response", err)
	}

	if got.GetStatus().GetContext() != analysis.StatusPartial {
		t.Errorf("context status = %q, want PARTIAL", got.GetStatus().GetContext())
	}
	if got.GetStatus().GetOverall() != analysis.RCAStatusPartial {
		t.Errorf("overall = %q, want PARTIAL", got.GetStatus().GetOverall())
	}
	// The topology-based rule still concluded. Withholding it because one HTTP
	// call failed would drop the answer precisely when it is needed.
	if ids := rootCauseIDs(got); len(ids) != 1 || ids[0] != "rc-tps-replica-degradation" {
		t.Errorf("root causes = %v, want only the replica degradation", ids)
	}

	missing := got.GetMeta().GetMissingContext()
	if len(missing) != 1 {
		t.Fatalf("missing context = %d, want 1", len(missing))
	}
	if missing[0].GetProvider() != analysis.ProviderConfiguration ||
		missing[0].GetKey() != "number_of_log_file" ||
		missing[0].GetReason() != analysis.ReasonRequestFailed {
		t.Errorf("missing context = %+v", missing[0])
	}
	if got.GetMeta().GetContextStatus() != got.GetStatus().GetContext() {
		t.Error("meta.context_status disagrees with status.context")
	}
}

func TestE2ENoMatchingProfileIsFailedPrecondition(t *testing.T) {
	db := testDB(t)
	client := startEngine(t, db)

	req := sipgwRequest()
	req.Alerts[0].ProbableCause = "SOMETHING_NO_PROFILE_MATCHES"
	req.Alerts = req.Alerts[:1]

	_, err := client.AnalyzeIncident(context.Background(), req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(err), err)
	}
	// The message is stable so a caller can tell which half of the
	// configuration is missing without parsing a wrapper.
	if msg := status.Convert(err).Message(); msg != "missing context_profile" {
		t.Errorf("message = %q, want \"missing context_profile\"", msg)
	}
}

func TestE2ENoEnabledRuleIsFailedPrecondition(t *testing.T) {
	db := testDB(t)
	if _, err := db.ExecContext(context.Background(), "UPDATE rca_rule SET enabled = FALSE"); err != nil {
		t.Fatal(err)
	}
	client := startEngine(t, db)

	_, err := client.AnalyzeIncident(context.Background(), sipgwRequest())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(err), err)
	}
	if msg := status.Convert(err).Message(); msg != "missing rca_rule" {
		t.Errorf("message = %q, want \"missing rca_rule\"", msg)
	}
}

func TestE2ENoConclusionIsASuccessfulResponse(t *testing.T) {
	// A profile matches, so the context is built; no rule has anything to say
	// about it. That is a statement about the rule set, not a failure.
	db := testDB(t)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE rca_rule SET enabled = FALSE WHERE name <> 'tps_overloaded'"); err != nil {
		t.Fatal(err)
	}
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), sipgwRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident = %v, want a successful response", err)
	}
	if got.GetStatus().GetOverall() != analysis.RCAStatusNoConclusion {
		t.Errorf("overall = %q, want NO_CONCLUSION", got.GetStatus().GetOverall())
	}
	if len(got.GetRca().GetRootCauses()) != 0 {
		t.Errorf("root causes = %d, want 0", len(got.GetRca().GetRootCauses()))
	}
	if got.GetStatus().GetContext() != analysis.StatusComplete {
		t.Errorf("context = %q, want COMPLETE", got.GetStatus().GetContext())
	}
}

func TestE2EOneBrokenRuleRowLeavesTheOthersStanding(t *testing.T) {
	db := testDB(t)
	// Corrupt one row's GRL. The row fails to compile; the SIPGW row must
	// still answer.
	if _, err := db.ExecContext(context.Background(),
		"UPDATE rca_rule SET rule_content = 'rule Broken { when then }' WHERE name = 'tps_overloaded'",
	); err != nil {
		t.Fatal(err)
	}
	client := startEngine(t, db)

	got, err := client.AnalyzeIncident(context.Background(), sipgwRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident = %v, want a successful PARTIAL response", err)
	}
	if got.GetStatus().GetRca() != analysis.RCAStatusPartial {
		t.Errorf("rca = %q, want PARTIAL", got.GetStatus().GetRca())
	}
	if len(got.GetRca().GetRootCauses()) != 3 {
		t.Errorf("root causes = %d, want 3", len(got.GetRca().GetRootCauses()))
	}
	// A failed rule is an operator's problem with the rule set, not a gap in
	// the context. It must not appear as missing context.
	if n := len(got.GetMeta().GetMissingContext()); n != 0 {
		t.Errorf("missing context = %d, want 0", n)
	}
	if got.GetMeta().GetContextStatus() != analysis.StatusComplete {
		t.Errorf("context = %q, want COMPLETE", got.GetMeta().GetContextStatus())
	}
}

func TestE2EEveryRuleRowBrokenIsInternal(t *testing.T) {
	db := testDB(t)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE rca_rule SET rule_content = 'rule Broken { when then }'"); err != nil {
		t.Fatal(err)
	}
	client := startEngine(t, db)

	_, err := client.AnalyzeIncident(context.Background(), sipgwRequest())
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s, want Internal (err=%v)", status.Code(err), err)
	}
	// Rule content must not travel to the caller.
	if msg := status.Convert(err).Message(); strings.Contains(msg, "rule Broken") {
		t.Errorf("message leaks rule content: %q", msg)
	}
}

func TestE2ECallerDeadlineIsHonoured(t *testing.T) {
	db := testDB(t)
	client := startEngine(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	_, err := client.AnalyzeIncident(ctx, sipgwRequest())
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("code = %s, want DeadlineExceeded (err=%v)", status.Code(err), err)
	}
}

func TestE2ESlowConfigurationAPIDegradesRatherThanHangs(t *testing.T) {
	// The per-call configuration timeout is the engine's own budget, separate
	// from the caller's. An API that never answers must produce a PARTIAL
	// snapshot, not a request that runs until the caller gives up.
	db := testDB(t)

	blocked := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() {
		close(blocked)
		slow.Close()
	})
	repointConfigurationURL(t, db, slow.URL+"/v1/num_of_log_file")

	cfg := config{
		ConfigurationTimeout: 100 * time.Millisecond,
		RCARuleTimeout:       800 * time.Millisecond,
	}
	client := startEngineWithConfig(t, db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := client.AnalyzeIncident(ctx, tpsRequest())
	if err != nil {
		t.Fatalf("AnalyzeIncident = %v, want a PARTIAL response", err)
	}
	if got.GetStatus().GetContext() != analysis.StatusPartial {
		t.Errorf("context = %q, want PARTIAL", got.GetStatus().GetContext())
	}
	missing := got.GetMeta().GetMissingContext()
	if len(missing) != 1 || missing[0].GetReason() != analysis.ReasonTimeout {
		t.Errorf("missing context = %+v, want one TIMEOUT", missing)
	}
}

// --- helpers -------------------------------------------------------------

// startEngineWithConfig is startEngine with the timeouts under test.
func startEngineWithConfig(t *testing.T, db *sql.DB, cfg config) mdafv1.IncidentAnalysisEngineClient {
	t.Helper()

	server, err := buildServer(cfg, db, testLogger())
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, testLogger(), server, listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		cancel()
		<-served
	})

	return mdafv1.NewIncidentAnalysisEngineClient(conn)
}

func rootCauseIDs(resp *mdafv1.AnalyzeIncidentResponse) []string {
	causes := resp.GetRca().GetRootCauses()
	out := make([]string, 0, len(causes))
	for _, c := range causes {
		out = append(out, c.GetId())
	}
	return out
}

// assertGoldenResponse compares the canonicalised response with the fixture.
//
// protojson deliberately varies its whitespace between runs, so its output is
// re-encoded through encoding/json: that fixes both the spacing and the key
// order, which is what makes a byte comparison meaningful. The response itself
// carries no timestamp and no latency, so there is nothing else to normalise.
func assertGoldenResponse(t *testing.T, scenario string, resp *mdafv1.AnalyzeIncidentResponse) {
	t.Helper()

	raw, err := protojson.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatalf("canonicalise response: %v", err)
	}
	encoded, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Join("..", "..", "testdata", "engine", scenario)
	path := filepath.Join(dir, "response.json")

	if os.Getenv("RE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with RE_UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if string(encoded) != string(want) {
		t.Errorf("response does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, encoded)
	}
}
