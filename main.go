// Copyright (c) 2024-2026 AccelByte Inc. All Rights Reserved.
// This is licensed software from AccelByte Inc, for limitations
// and restrictions contact your company contract manager.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"extend-player-hosted-server/pkg/client/playerhosted"
	"extend-player-hosted-server/pkg/common"
	"extend-player-hosted-server/pkg/config"
	sessiondsm "extend-player-hosted-server/pkg/pb"
	serverPlayerHosted "extend-player-hosted-server/pkg/server/playerhosted"

	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/factory"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/repository"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/service/iam"
	"github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/service/session"
	sdkAuth "github.com/AccelByte/accelbyte-go-sdk/services-api/pkg/utils/auth"
	"github.com/caarlos0/env"
	promgrpc "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/prometheus/client_golang/prometheus"
	prometheusCollectors "github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"

	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

const (
	id              = int64(1)
	environment     = "production"
	metricsEndpoint = "/metrics"
	metricsPort     = 8080
	grpcPort        = 6565
)

var (
	serviceName = "extend-app-session-dsm"
	logLevelStr = common.GetEnv("LOG_LEVEL", "info")
)

func parseSlogLevel(levelStr string) slog.Level {
	switch strings.ToLower(levelStr) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	go func() {
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(10)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Parse log level from environment variable
	slogLevel := parseSlogLevel(logLevelStr)

	// Create JSON handler for structured logging
	opts := &slog.HandlerOptions{
		Level: slogLevel,
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger) // Set as default logger for the application

	logger.Info("starting app server..")

	loggingOptions := []logging.Option{
		logging.WithLogOnEvents(logging.StartCall, logging.FinishCall, logging.PayloadReceived, logging.PayloadSent),
		logging.WithFieldsFromContext(func(ctx context.Context) logging.Fields {
			if span := trace.SpanContextFromContext(ctx); span.IsSampled() {
				return logging.Fields{"traceID", span.TraceID().String()}
			}

			return nil
		}),
		logging.WithLevels(logging.DefaultClientCodeToLevel),
		logging.WithDurationField(logging.DurationToDurationField),
	}

	srvMetrics := promgrpc.NewServerMetrics()
	unaryServerInterceptors := []grpc.UnaryServerInterceptor{
		srvMetrics.UnaryServerInterceptor(),
		logging.UnaryServerInterceptor(common.InterceptorLogger(logger), loggingOptions...),
	}
	streamServerInterceptors := []grpc.StreamServerInterceptor{
		srvMetrics.StreamServerInterceptor(),
		logging.StreamServerInterceptor(common.InterceptorLogger(logger), loggingOptions...),
	}

	// Set Tracer Provider
	if val := common.GetEnv("OTEL_SERVICE_NAME", ""); val != "" {
		serviceName = "extend-app-sd-" + strings.ToLower(val)
	}
	tracerProvider, err := common.NewTracerProvider(serviceName, environment, id)
	if err != nil {
		logger.Error("failed to create tracer provider", "error", err)
		os.Exit(1)
	}

	otel.SetTracerProvider(tracerProvider)
	defer func(ctx context.Context) {
		if err := tracerProvider.Shutdown(ctx); err != nil {
			logger.Error("failed to shutdown tracer provider", "error", err)
			os.Exit(1)
		}
	}(ctx)
	logger.Info("set tracer provider", "name", serviceName, "environment", environment, "id", id)

	// Set Text Map Propagator
	b := b3.New(b3.WithInjectEncoding(b3.B3MultipleHeader))
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			b,
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	// Set tracing HTTP transport globally so all outgoing HTTP calls are traced
	http.DefaultTransport = common.NewTracingRoundTripper()

	// Preparing the IAM authorization
	var tokenRepo repository.TokenRepository = sdkAuth.DefaultTokenRepositoryImpl()
	var configRepo repository.ConfigRepository = sdkAuth.DefaultConfigRepositoryImpl()
	var refreshRepo repository.RefreshTokenRepository = &sdkAuth.RefreshTokenImpl{RefreshRate: 0.8, AutoRefresh: true}

	oauthService := iam.OAuth20Service{
		Client:                 factory.NewIamClient(configRepo),
		TokenRepository:        tokenRepo,
		RefreshTokenRepository: refreshRepo,
		ConfigRepository:       configRepo,
	}

	gameSessionService := session.GameSessionService{
		Client:           factory.NewSessionClient(configRepo),
		TokenRepository:  tokenRepo,
		ConfigRepository: configRepo,
	}

	if strings.ToLower(common.GetEnv("PLUGIN_GRPC_SERVER_AUTH_ENABLED", "true")) == "true" {
		refreshInterval := common.GetEnvInt("REFRESH_INTERVAL", 600)
		common.Validator = common.NewTokenValidator(oauthService, time.Duration(refreshInterval)*time.Second, true)
		err := common.Validator.Initialize(ctx)
		if err != nil {
			logger.Info(err.Error())
		}

		unaryServerInterceptors = append(unaryServerInterceptors, common.UnaryAuthServerIntercept)
		streamServerInterceptors = append(streamServerInterceptors, common.StreamAuthServerIntercept)
		logger.Info("added auth interceptors")
	}

	cfg := &config.Config{}

	flag.Usage = func() {
		flag.CommandLine.SetOutput(os.Stdout)
		for _, val := range cfg.HelpDocs() {
			//nolint:forbidigo
			fmt.Println(val)
		}

		//nolint:forbidigo
		fmt.Println("")
		flag.PrintDefaults()
	}
	flag.Parse()

	err = env.Parse(cfg)
	if err != nil {
		logger.Error("unable to parse environment variables", "error", err)

		return
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unaryServerInterceptors...),
		grpc.ChainStreamInterceptor(streamServerInterceptors...),
	)

	registry := playerhosted.New(cfg, &gameSessionService)
	playerHostedServer := &serverPlayerHosted.SessionDSM{
		UnimplementedSessionDsmServer: sessiondsm.UnimplementedSessionDsmServer{},
		Registry:                      registry,
	}
	sessiondsm.RegisterSessionDsmServer(grpcServer, playerHostedServer)

	// Enforce the persistent-session lifecycle: report DS_ERROR for hosts that
	// stop heartbeating and delete sessions that stay hostless or empty too long.
	go registry.StartHeartbeatMonitor(ctx)

	// Host-facing HTTP registration API. Hosts push their public IP:port here; it
	// must be exposed via ingress. Uses its own mux so pprof is not exposed on
	// this port.
	regHandler := serverPlayerHosted.NewRegistrationHandler(registry, &gameSessionService, cfg)
	go func() {
		regAddr := fmt.Sprintf(":%d", cfg.PlayerHostedRegPort)
		logger.Info("starting player-hosted registration HTTP server", "addr", regAddr)
		if errReg := http.ListenAndServe(regAddr, regHandler.Mux()); errReg != nil {
			logger.Error("player-hosted registration server failed", "error", errReg)
		}
	}()

	// Enable gRPC Reflection
	reflection.Register(grpcServer)
	logger.Info("gRPC reflection enabled")

	// Enable gRPC health check
	grpc_health_v1.RegisterHealthServer(grpcServer, health.NewServer())

	// Register Prometheus Metrics
	srvMetrics.InitializeMetrics(grpcServer)
	prometheusRegistry := prometheus.NewRegistry()
	prometheusRegistry.MustRegister(
		prometheusCollectors.NewGoCollector(),
		prometheusCollectors.NewProcessCollector(prometheusCollectors.ProcessCollectorOpts{}),
		srvMetrics,
	)

	go func() {
		http.Handle(metricsEndpoint, promhttp.HandlerFor(prometheusRegistry, promhttp.HandlerOpts{}))
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), nil))
	}()
	logger.Info("serving prometheus metrics", "port", metricsPort, "endpoint", metricsEndpoint)

	logger.Info("set text map propagator")

	// Start gRPC Server
	logger.Info("starting gRPC server..")
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		logger.Error("failed to listen to tcp", "port", grpcPort, "error", err)
		os.Exit(1)
	}
	go func() {
		if err = grpcServer.Serve(lis); err != nil {
			logger.Error("failed to run gRPC server", "error", err)
			os.Exit(1)
		}
	}()
	logger.Info("gRPC server started")
	logger.Info("app server started")

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	logger.Info("signal received")
}
