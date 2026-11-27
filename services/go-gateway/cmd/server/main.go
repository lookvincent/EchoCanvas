package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/client"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/config"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/gateway"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/handler"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: config.ParseLogLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	gin.SetMode(cfg.GinMode)

	initCtx, cancelInit := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelInit()

	pool, err := repository.NewPool(initCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(initCtx).Err(); err != nil {
		logger.Error("failed to connect to redis", "addr", cfg.RedisAddr, "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{Addr: cfg.RedisAddr})
	defer func() { _ = asynqClient.Close() }()

	projectRepo := repository.NewPostgresProjectRepository(pool)
	assetRepo := repository.NewPostgresAssetRepository(pool)
	jobRepo := repository.NewPostgresJobRepository(pool)
	usageRepo := repository.NewPostgresUsageRepository(pool)
	broker := sse.NewRedisBroker(rdb)
	pyClient := client.NewPySidecarClient(cfg.PySidecarURL, cfg.InternalAuthToken, nil)
	workerAdapter := client.NewSidecarWorkerAdapter(pyClient, projectRepo, jobRepo, usageRepo, broker)
	sched := scheduler.New(
		jobRepo,
		scheduler.NewAsynqEnqueuer(asynqClient),
		broker,
		workerAdapter,
		workerAdapter,
		cfg.MaxConcurrentVision,
		logger,
	)
	gw := gateway.New(usageRepo, broker, cfg, gateway.Options{Logger: logger})

	asynqSrv, asynqMux := scheduler.NewAsynqServer(cfg.RedisAddr, sched)
	go func() {
		if err := asynqSrv.Run(asynqMux); err != nil {
			logger.Error("asynq worker stopped", "error", err)
		}
	}()
	defer asynqSrv.Shutdown()

	router := handler.NewRouterWithDeps(handler.RouterDeps{
		Logger:            logger,
		PGPinger:          pool,
		ProjectRepo:       projectRepo,
		AssetRepo:         assetRepo,
		JobHandler:        handler.NewJobHandlerWithSidecar(jobRepo, sched, pyClient, usageRepo, broker),
		SSEHandler:        handler.NewSSEHandler(jobRepo, broker),
		GatewayHandler:    handler.NewGatewayHandler(gw, usageRepo, broker),
		InternalAuthToken: cfg.InternalAuthToken,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("go-gateway listening", "port", cfg.Port, "py_sidecar", cfg.PySidecarURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down go-gateway")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("go-gateway stopped")
}
