package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/api"
	"github.com/nchapman/hivebot/internal/config"
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

	swarmCode := envOr("HIVE_SWARM_CODE", "default")
	listenAddr := envOr("HIVE_ADDR", ":8080")
	agentsDir := envOr("HIVE_AGENTS_DIR", "agents")
	providerType := envOr("HIVE_PROVIDER", "anthropic")
	apiKey := os.Getenv("HIVE_API_KEY")
	modelOverride := os.Getenv("HIVE_MODEL")

	swarm := hub.NewSwarm(swarmCode)

	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("loading web UI: %w", err)
	}

	srv := api.NewServer(swarm, logger, webFS)

	// Load coordinator agent if config exists and API key is set
	coordinatorDir := filepath.Join(agentsDir, "coordinator")
	if apiKey != "" {
		if _, err := os.Stat(filepath.Join(coordinatorDir, "agent.md")); err == nil {
			cfg, err := config.LoadAgentDir(coordinatorDir)
			if err != nil {
				return fmt.Errorf("loading coordinator config: %w", err)
			}

			coordinator, err := agent.New(context.Background(), cfg, swarm, agent.Options{
				Provider: agent.ProviderType(providerType),
				APIKey:   apiKey,
				Model:    modelOverride,
			}, logger)
			if err != nil {
				return fmt.Errorf("creating coordinator agent: %w", err)
			}

			// Wire task dispatch through the transport layer
			coordinator.SetTaskDispatcher(srv.Transport().DispatchTask)

			logger.Info("coordinator agent loaded",
				"name", coordinator.Name(),
				"model", cfg.Model,
				"skills", len(cfg.Skills),
			)

			// Wire coordinator into chat WebSocket endpoint
			srv.SetAgent(coordinator)
		}
	} else {
		logger.Info("no HIVE_API_KEY set — running without LLM (dashboard only)")
	}

	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: srv,
		// No read/write timeout — WebSocket connections are long-lived
		IdleTimeout: 120 * time.Second,
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
