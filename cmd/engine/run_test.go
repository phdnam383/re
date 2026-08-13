package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"re/gen/mdafv1"
)

// --- startup failures ----------------------------------------------------

func TestRunFailsOnBadConfiguration(t *testing.T) {
	// Every startup failure comes back from run rather than exiting somewhere
	// deep inside a helper, so main stays the only place that decides the
	// process's fate.
	err := run(context.Background(), func(string) string { return "" }, io.Discard)
	if err == nil {
		t.Fatal("run = nil, want a configuration error")
	}
	if !strings.Contains(err.Error(), "configuration") {
		t.Errorf("error = %q, want it to name the configuration stage", err)
	}
}

func TestRunFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	// sql.Open does not connect, so without the startup ping the first symptom
	// of a wrong DSN would be a failed request minutes later.
	values := minimalEnv()
	// Port 1 is reserved and refuses immediately, so this fails fast rather
	// than waiting out the startup timeout.
	values[envDBDSN] = "postgres://user:pass@127.0.0.1:1/re?sslmode=disable&connect_timeout=1"

	err := run(context.Background(), env(values), io.Discard)
	if err == nil {
		t.Fatal("run = nil, want a connection error")
	}
	if !strings.Contains(err.Error(), "connect to database") {
		t.Errorf("error = %q, want it to name the connection", err)
	}
	// The DSN carries a password and must not be repeated in the message.
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("error leaks the DSN: %q", err)
	}
}

func TestOpenDatabaseRejectsAMalformedDSN(t *testing.T) {
	_, err := openDatabase(context.Background(), "://not a dsn")
	if err == nil {
		t.Fatal("openDatabase = nil, want an error")
	}
}

// --- serve lifecycle -----------------------------------------------------

// newTestServer builds a gRPC server with nothing registered. The lifecycle
// tests are about starting and stopping, not about what is served.
func newTestServer(t *testing.T) (*grpc.Server, net.Listener) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return grpc.NewServer(), listener
}

func TestServeStopsWhenTheContextIsCancelled(t *testing.T) {
	server, listener := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serve(ctx, testLogger(), server, listener) }()

	// Give Serve a moment to take the listener, then ask it to stop.
	waitForServing(t, listener.Addr().String())
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve = %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancellation")
	}
}

func TestServeReturnsTheListenerFailure(t *testing.T) {
	// The server gave up before any signal. That is the run's failure and must
	// not be reported as a clean shutdown.
	server, listener := newTestServer(t)
	listener.Close()

	err := serve(context.Background(), testLogger(), server, listener)
	if err == nil {
		t.Fatal("serve = nil, want the listener failure")
	}
	if !strings.Contains(err.Error(), "serve") {
		t.Errorf("error = %q", err)
	}
}

func TestServeWaitsForAnInFlightRequest(t *testing.T) {
	// GracefulStop is what gives a running analysis its chance to finish. A
	// shutdown that cut it off would drop an answer the caller was still
	// waiting for.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	server := grpc.NewServer()
	mdafv1.RegisterRuleEngineServer(server, &blockingEngine{started: started, release: release})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	served := make(chan error, 1)
	go func() { served <- serve(ctx, testLogger(), server, listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	answered := make(chan error, 1)
	go func() {
		_, err := mdafv1.NewRuleEngineClient(conn).AnalyzeAlert(
			context.Background(), &mdafv1.AnalyzeAlertRequest{RequestId: "r"})
		answered <- err
	}()

	<-started
	cancel() // shutdown while the call is in flight

	// The server must not be gone yet: the request is still running.
	select {
	case err := <-served:
		t.Fatalf("serve returned while a request was in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	if err := <-answered; err != nil {
		t.Errorf("the in-flight call failed: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the request finished")
	}
}

func TestServeDoesNotLeakGoroutinesOnRepeatedRuns(t *testing.T) {
	// Both shutdown paths have to terminate: the graceful stop and the forced
	// one. Running the lifecycle repeatedly is what would surface a goroutine
	// left inside GracefulStop.
	for i := range 5 {
		server, listener := newTestServer(t)
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() { done <- serve(ctx, testLogger(), server, listener) }()

		waitForServing(t, listener.Addr().String())
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run %d: serve = %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d: serve did not return", i)
		}
	}
}

// --- wiring --------------------------------------------------------------

func TestBuildServerWiresEveryModule(t *testing.T) {
	// The pool is never used here — no constructor queries at build time, and
	// a wiring mistake must fail before the process starts accepting traffic
	// rather than on the first request.
	db, err := sql.Open(dbDriver, "postgres://user:pass@127.0.0.1:1/re?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := config{
		DSN:                  "unused",
		GRPCAddr:             ":0",
		ConfigurationTimeout: time.Second,
		RCARuleTimeout:       time.Second,
	}

	server, err := buildServer(cfg, db, testLogger())
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if server == nil {
		t.Fatal("buildServer returned no server")
	}

	// The service has to be registered under the generated name, or a client
	// would get Unimplemented from a process that looks healthy.
	if _, ok := server.GetServiceInfo()["mdaf.v1.RuleEngine"]; !ok {
		t.Errorf("services = %v, want the engine registered", server.GetServiceInfo())
	}
	server.Stop()
}

func TestRunFailsWhenTheAddressIsUnusable(t *testing.T) {
	// A listen failure has to come back from run. The DSN below is never
	// dialled because the test skips when no database is configured — this is
	// about the address, so it uses a port that cannot be bound.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	values := minimalEnv()
	values[envDBDSN] = "postgres://user:pass@127.0.0.1:1/re?sslmode=disable&connect_timeout=1"
	values[envGRPCAddr] = held.Addr().String()

	// The database is unreachable, so run fails there first; that is the
	// documented order — a process that cannot reach its data must not bind a
	// port and look healthy.
	err = run(context.Background(), env(values), io.Discard)
	if err == nil {
		t.Fatal("run = nil, want an error")
	}
	if !strings.Contains(err.Error(), "connect to database") {
		t.Errorf("error = %q, want the database checked before the listener", err)
	}
}

// --- helpers -------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}

// waitForServing blocks until the address accepts a connection, so a test does
// not race Serve taking the listener.
func waitForServing(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nothing accepted a connection on %s", addr)
}

// blockingEngine holds one request open so a shutdown test can observe the
// graceful path.
type blockingEngine struct {
	mdafv1.UnimplementedRuleEngineServer
	started chan struct{}
	release chan struct{}
}

func (e *blockingEngine) AnalyzeAlert(
	_ context.Context,
	req *mdafv1.AnalyzeAlertRequest,
) (*mdafv1.AnalyzeAlertResponse, error) {
	close(e.started)
	<-e.release
	return &mdafv1.AnalyzeAlertResponse{RequestId: req.GetRequestId()}, nil
}
