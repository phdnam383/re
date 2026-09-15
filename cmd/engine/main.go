package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/contextbuilder/configuration"
	"re/internal/contextbuilder/link"
	"re/internal/contextbuilder/metric"
	cbpostgres "re/internal/contextbuilder/postgres"
	"re/internal/contextbuilder/vdu"
	"re/internal/profilemanagement"
	pmpostgres "re/internal/profilemanagement/postgres"
	"re/internal/ruleengine"
	repostgres "re/internal/ruleengine/postgres"
	"re/internal/rulemanagement"
	rmpostgres "re/internal/rulemanagement/postgres"
	transportgrpc "re/internal/transport/grpc"
	transporthttp "re/internal/transport/http"
)

func main() {
	const (
		envDBDSN                = "DATABASE_URL"
		envGRPCAddr             = "RE_GRPC_ADDR"
		envConfigurationTimeout = "RE_CONFIGURATION_TIMEOUT"
		envLinkTimeout          = "RE_PROBE_TIMEOUT"
		envVDUTimeout           = "RE_VDU_TIMEOUT"
		envMetricTimeout        = "RE_METRIC_TIMEOUT"
		envRCARuleTimeout       = "RE_RCA_RULE_TIMEOUT"
		envConfigurationBaseURL = "RE_CONFIGURATION_BASE_URL"
		envLinkBaseURL          = "RE_PROBE_BASE_URL"
		envVDUBaseURL           = "RE_VDU_BASE_URL"
		envMetricBaseURL        = "RE_METRIC_BASE_URL"
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

	linkTimeout := link.DefaultTimeout
	if raw := os.Getenv(envLinkTimeout); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %q is not a duration: %v\n", envLinkTimeout, raw, err)
			os.Exit(1)
		}
		if value <= 0 {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %v must be greater than zero\n", envLinkTimeout, value)
			os.Exit(1)
		}
		linkTimeout = value
	}

	vduTimeout := vdu.DefaultTimeout
	if raw := os.Getenv(envVDUTimeout); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %q is not a duration: %v\n", envVDUTimeout, raw, err)
			os.Exit(1)
		}
		if value <= 0 {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %v must be greater than zero\n", envVDUTimeout, value)
			os.Exit(1)
		}
		vduTimeout = value
	}

	metricTimeout := metric.DefaultTimeout
	if raw := os.Getenv(envMetricTimeout); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %q is not a duration: %v\n", envMetricTimeout, raw, err)
			os.Exit(1)
		}
		if value <= 0 {
			fmt.Fprintf(os.Stderr, "engine: configuration: %s: %v must be greater than zero\n", envMetricTimeout, value)
			os.Exit(1)
		}
		metricTimeout = value
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

	configurationBaseURL := os.Getenv(envConfigurationBaseURL)
	linkBaseURL := os.Getenv(envLinkBaseURL)
	vduBaseURL := os.Getenv(envVDUBaseURL)
	metricBaseURL := os.Getenv(envMetricBaseURL)

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
		VDU: vdu.New(vdu.Options{
			Timeout: vduTimeout,
			BaseURL: vduBaseURL,
		}),
		Configuration: configuration.New(configuration.Options{
			Timeout: configurationTimeout,
			BaseURL: configurationBaseURL,
		}),
		Link: link.New(link.Options{
			Timeout: linkTimeout,
			BaseURL: linkBaseURL,
			Logger:  logger,
		}),
		Metric: metric.New(metric.Options{
			Timeout: metricTimeout,
			BaseURL: metricBaseURL,
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

	httpAddr := os.Getenv("RE_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	httpServer := transporthttp.NewServer(httpAddr, rulemanagement.NewService(rmpostgres.NewRepository(db)), profilemanagement.NewService(pmpostgres.NewRepository(db)), logger)
	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		listener.Close()
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: listen HTTP:", err)
		os.Exit(1)
	}
	logger.Info("management REST listening", "address", httpListener.Addr().String())

	logger.Info("engine listening",
		"address", listener.Addr().String(),
		"configuration_timeout", configurationTimeout.String(),
		"probe_timeout", linkTimeout.String(),
		"vdu_timeout", vduTimeout.String(),
		"metric_timeout", metricTimeout.String(),
		"rca_rule_timeout", rcaRuleTimeout.String(),
	)

	served := make(chan error, 2)
	go func() { served <- server.Serve(listener) }()
	go func() { served <- httpServer.Serve(httpListener) }()

	var serveErr error
	completed := 0
	select {
	case serveErr = <-served:
		completed++
	case <-ctx.Done():
		logger.Info("shutdown signal received", "grace_period", shutdownGrace.String())
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelShutdown()
	stopped := make(chan struct{})
	go func() { server.GracefulStop(); close(stopped) }()
	httpStopped := make(chan struct{})
	go func() {
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
		}
		close(httpStopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		server.Stop()
		<-stopped
	}
	<-httpStopped
	for completed < 2 {
		err := <-served
		completed++
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) && !errors.Is(serveErr, http.ErrServerClosed) {
		db.Close()
		fmt.Fprintln(os.Stderr, "engine: serve:", serveErr)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
