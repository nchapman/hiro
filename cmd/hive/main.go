package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nchapman/hivebot/internal/api"
	"github.com/nchapman/hivebot/internal/hub"
	"github.com/nchapman/hivebot/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// For now, use a default swarm code. Will be configurable.
	swarmCode := envOr("HIVE_SWARM_CODE", "default")
	listenAddr := envOr("HIVE_ADDR", ":8080")

	swarm := hub.NewSwarm(swarmCode)

	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("loading web UI: %w", err)
	}

	srv := api.NewServer(swarm, logger, webFS)

	httpServer := &http.Server{
		Addr:         listenAddr,
		Handler:      srv,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("hive starting", "addr", listenAddr, "swarm", swarmCode)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	return httpServer.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
