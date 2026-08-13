// Test-only compatibility seams for the existing lifecycle and composition
// tests. None of these helpers is compiled into the engine binary.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"

	// The pgx stdlib driver is registered here and only here. Every package
	// below depends on database/sql alone, so the driver choice stays a
	// deployment decision made in the composition root.
	_ "github.com/jackc/pgx/v5/stdlib"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/contextbuilder/configuration"
	cbpostgres "re/internal/contextbuilder/postgres"
	"re/internal/ruleengine"
	repostgres "re/internal/ruleengine/postgres"
	transportgrpc "re/internal/transport/grpc"
)

const (
	// dbDriver is fixed. Making it configurable would let a deployment point
	// the engine at a driver whose ltree and JSONB handling has never been
	// tested against these queries.
	dbDriver = "pgx"

	// startupTimeout bounds the first connection. A database that is not
	// reachable at startup fails the process rather than letting it come up
	// and reject every request — an engine that is listening is expected to
	// work.
	startupTimeout = 5 * time.Second

	// shutdownGrace is how long in-flight requests get to finish after a
	// signal. Finite on purpose: a request stuck on an unresponsive
	// configuration API would otherwise hold the process open forever, and a
	// deployment that cannot be restarted is worse than one that drops a call.
	shutdownGrace = 10 * time.Second
)

// run is the whole program. main only reports what it returns.
//
// getenv and stdout are parameters rather than package-level lookups so the
// startup path is testable end to end without mutating the process
// environment, which parallel tests share.
func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	// One JSON logger, injected into every module. A single handler is what
	// makes a request traceable across the builder, the rule engine and the
	// transport in one stream.
	logger := slog.New(slog.NewJSONHandler(stdout, nil))

	db, err := openDatabase(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer db.Close()

	server, err := buildServer(cfg, db, logger)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}

	logger.Info("engine listening",
		"address", listener.Addr().String(),
		"configuration_timeout", cfg.ConfigurationTimeout.String(),
		"rca_rule_timeout", cfg.RCARuleTimeout.String(),
	)
	return serve(ctx, logger, server, listener)
}

// openDatabase opens the shared pool and proves it works.
//
// The ping is the point. sql.Open does not connect, so without it the first
// symptom of a wrong DSN would be a failed request minutes later, reported to
// a caller as an internal error.
func openDatabase(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open(dbDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		// The DSN is not repeated in the message: it carries the password.
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

// buildServer wires every module and returns a gRPC server ready to serve.
//
// This is the only function in the tree that knows which implementation
// satisfies which port. Everything it constructs shares one *sql.DB, because a
// second pool against the same database would double the connection budget for
// no isolation — the stages read different tables, not different data.
func buildServer(cfg config, db *sql.DB, logger *slog.Logger) (*grpc.Server, error) {
	builder, err := contextbuilder.New(contextbuilder.Options{
		Profiles: cbpostgres.NewProfileRepository(db),
		VDU:      cbpostgres.NewVDUProvider(db),
		Link:     cbpostgres.NewLinkProvider(db),
		Configuration: configuration.New(configuration.Options{
			Timeout: cfg.ConfigurationTimeout,
		}),
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build context builder: %w", err)
	}

	engine, err := ruleengine.New(ruleengine.Options{
		Rules:       repostgres.NewRuleRepository(db),
		RuleTimeout: cfg.RCARuleTimeout,
		Logger:      logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build rule engine: %w", err)
	}

	service, err := analysis.NewService(analysis.ServiceOptions{
		Context: builder,
		RCA:     engine,
		Logger:  logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build analysis service: %w", err)
	}

	transport, err := transportgrpc.NewServer(service, logger)
	if err != nil {
		return nil, fmt.Errorf("build transport: %w", err)
	}

	server := grpc.NewServer(
		grpc.UnaryInterceptor(transportgrpc.LoggingInterceptor(logger)),
	)
	transport.Register(server)
	return server, nil
}

// serve runs until the context is cancelled or the server stops on its own.
//
// Two shutdown paths, and both have to terminate. A signal starts a graceful
// stop so in-flight analyses finish; if they do not finish inside the grace
// period, Stop forces the issue. Waiting on GracefulStop alone would make the
// process ignore the second SIGTERM an operator sends when the first appears
// to have done nothing.
func serve(ctx context.Context, logger *slog.Logger, server *grpc.Server, listener net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case err := <-served:
		// The server gave up before any signal — a listener that closed, an
		// unrecoverable transport error. That is the run's failure.
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received", "grace_period", shutdownGrace.String())
	}

	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()

	select {
	case <-stopped:
		logger.Info("shutdown complete")
	case <-timer.C:
		logger.Warn("grace period expired; forcing shutdown")
		server.Stop()
		// Stop unblocks GracefulStop, so this returns rather than leaking the
		// goroutine that is still inside it.
		<-stopped
	}

	if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
