package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/agent"
	"github.com/sumartiot/dozzle-log-archive/internal/config"
	"github.com/sumartiot/dozzle-log-archive/internal/ingest"
	"github.com/sumartiot/dozzle-log-archive/internal/live"
	"github.com/sumartiot/dozzle-log-archive/internal/registry"
	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

const (
	batchSize       = 1000
	batchInterval   = 2 * time.Second
	registryRefresh = 30 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("log archive stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	store, err := storage.New(ctx, cfg.DatabaseURL, cfg.RetentionMonths, cfg.MaxRowsPerQuery, logger)
	if err != nil {
		return err
	}
	defer store.Close()

	broker := live.NewBroker(logger)
	serviceRegistry := registry.New(store, registryRefresh, logger)
	batcher := ingest.NewBatcher(
		store,
		cfg.MaxPendingRows,
		batchSize,
		batchInterval,
		func(rows []storage.LogRow) {
			serviceRegistry.Observe(rows)
			broker.Publish(rows)
		},
		logger,
	)

	agentService := agent.NewService(store, serviceRegistry, broker)
	grpcServer, err := agent.NewGRPCServer(agentService, cfg.DozzleCert, cfg.DozzleKey)
	if err != nil {
		return err
	}
	grpcListener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	defer grpcListener.Close()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           ingest.NewHTTPHandler(cfg.IngestToken, batcher, store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	go store.RunMaintenance(ctx)
	go serviceRegistry.Run(ctx)
	batchCtx, batchCancel := context.WithCancel(context.Background())
	defer batchCancel()
	batchDone := make(chan struct{})
	go func() {
		batcher.Run(batchCtx)
		close(batchDone)
	}()

	errCh := make(chan error, 2)
	go func() {
		logger.Info("Dozzle archive agent listening", "address", cfg.GRPCAddr, "protocol", agent.DozzleProtocolVersion)
		if err := grpcServer.Serve(grpcListener); err != nil {
			errCh <- err
		}
	}()
	go func() {
		logger.Info("ingest HTTP server listening", "address", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		serveErr = err
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	batchCancel()

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}
	batchWait := time.NewTimer(35 * time.Second)
	defer batchWait.Stop()
	select {
	case <-batchDone:
	case <-batchWait.C:
		logger.Error("timed out waiting for ingest batch shutdown", "pending", batcher.Pending())
	}
	return serveErr
}
