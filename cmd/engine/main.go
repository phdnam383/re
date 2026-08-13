// Command engine is the composition root. It reads the environment, opens the
// database, wires the Context Builder, the RCA Rule Engine and the gRPC
// transport together, and serves until it is asked to stop.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/contextbuilder/configuration"
	cbpostgres "re/internal/contextbuilder/postgres"
	"re/internal/ruleengine"
	repostgres "re/internal/ruleengine/postgres"
	transportgrpc "re/internal/transport/grpc"
)

func main() {
	const (
		envDBDSN                = "RE_DB_DSN"
		envGRPCAddr             = "RE_GRPC_ADDR"
		envConfigurationTimeout = "RE_CONFIGURATION_TIMEOUT"
		envRCARuleTimeout       = "RE_RCA_RULE_TIMEOUT"
		dbDriver                = "pgx"
		startupTimeout          = 5 * time.Second
		shutdownGrace           = 10 * time.Second
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv(envDBDSN)
	if dsn == "" {
		fmt.Fprintf(os.Stderr, "engine: configuration: %s is required\n", envDBDSN)
		os.Exit(1)
	}

	grpcAddr := os.Getenv(envGRPCAddr)
	if grpcAddr == "" {
		fmt.Fprintf(os.Stderr, "engine: configuration: %s is required\n", envGRPCAddr)
		os.Exit(1)
	}

	configurationTimeout := configuration.DefaultTimeout
	if raw := os.Getenv(envConfigurationTimeout); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %q is not a duration: %v\n", envConfigurationTimeout, raw, err)
			os.Exit(1)
		}
		if value <= 0 {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %v must be greater than zero\n", envConfigurationTimeout, value)
			os.Exit(1)
		}
		configurationTimeout = value
	}

	rcaRuleTimeout := ruleengine.DefaultRuleTimeout
	if raw := os.Getenv(envRCARuleTimeout); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %q is not a duration: %v\n", envRCARuleTimeout, raw, err)
			os.Exit(1)
		}
		if value <= 0 {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %v must be greater than zero\n", envRCARuleTimeout, value)
			os.Exit(1)
		}
		rcaRuleTimeout = value
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	db, err := sql.Open(dbDriver, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "engine: open database:", err)
		os.Exit(1)
	}

	pingCtx, cancelPing := context.WithTimeout(ctx, startupTimeout)
	err = db.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: connect to database:", err)
		os.Exit(1)
	}
	defer db.Close()

	builder, err := contextbuilder.New(contextbuilder.Options{
		Profiles: cbpostgres.NewProfileRepository(db),
		VDU:      cbpostgres.NewVDUProvider(db),
		Link:     cbpostgres.NewLinkProvider(db),
		Configuration: configuration.New(configuration.Options{
			Timeout: configurationTimeout,
		}),
		Logger: logger,
	})
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: build context builder:", err)
		os.Exit(1)
	}

	rcaEngine, err := ruleengine.New(ruleengine.Options{
		Rules:       repostgres.NewRuleRepository(db),
		RuleTimeout: rcaRuleTimeout,
		Logger:      logger,
	})
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: build rule engine:", err)
		os.Exit(1)
	}

	service, err := analysis.NewService(analysis.ServiceOptions{
		Context: builder,
		RCA:     rcaEngine,
		Logger:  logger,
	})
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: build analysis service:", err)
		os.Exit(1)
	}

	transport, err := transportgrpc.NewServer(service, logger)
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: build transport:", err)
		os.Exit(1)
	}

	server := grpc.NewServer(
		grpc.UnaryInterceptor(transportgrpc.LoggingInterceptor(logger)),
	)
	transport.Register(server)

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "engine: listen on %s: %v\n", grpcAddr, err)
		os.Exit(1)
	}

	logger.Info("engine listening",
		"address", listener.Addr().String(),
		"configuration_timeout", configurationTimeout.String(),
		"rca_rule_timeout", rcaRuleTimeout.String(),
	)

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case err = <-served:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			db.Close()
			fmt.Fprintln(os.Stderr, "engine: serve:", err)
			os.Exit(1)
		}
		return
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
		<-stopped
	}

	if err = <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: serve:", err)
		os.Exit(1)
	}
}
