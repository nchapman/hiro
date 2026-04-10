package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/channel"
	slackchannel "github.com/nchapman/hiro/internal/channel/slack"
	telegramchannel "github.com/nchapman/hiro/internal/channel/telegram"
	"github.com/nchapman/hiro/internal/config"
)

// channelManager implements agent.InstanceLifecycleHook to create and destroy
// per-instance messaging channels (Telegram, Slack) based on the instance's
// config.yaml. This avoids import cycles between agent and channel packages.
type channelManager struct {
	mu       sync.Mutex
	channels map[string][]channel.Channel // instance ID -> channels for that instance

	router *channel.Router
	cp     agent.ControlPlane
	mux    *http.ServeMux
	logger *slog.Logger
}

func newChannelManager(router *channel.Router, cp agent.ControlPlane, mux *http.ServeMux, logger *slog.Logger) *channelManager {
	return &channelManager{
		channels: make(map[string][]channel.Channel),
		router:   router,
		cp:       cp,
		mux:      mux,
		logger:   logger.With("component", "channel-manager"),
	}
}

// OnInstanceStart reads the instance's config and starts any configured channels.
// Idempotent — safe to call multiple times for the same instance (e.g. during
// leader bootstrap where the hook fires from registerAndStartInstance and again
// from initChannels).
func (cm *channelManager) OnInstanceStart(ctx context.Context, instanceID, configPath string) error {
	// Idempotency guard: skip if channels are already running for this instance.
	cm.mu.Lock()
	if _, exists := cm.channels[instanceID]; exists {
		cm.mu.Unlock()
		return nil
	}
	cm.mu.Unlock()

	cfg, err := config.LoadInstanceConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading instance config: %w", err)
	}
	if cfg.Channels == nil {
		return nil
	}

	if tg := cfg.Channels.Telegram; tg != nil {
		ch, err := cm.startTelegram(ctx, instanceID, tg)
		if err != nil {
			cm.logger.Warn("failed to start telegram channel", "instance", instanceID, "error", err)
		} else if ch != nil {
			cm.trackChannel(instanceID, ch)
		}
	}

	if sl := cfg.Channels.Slack; sl != nil {
		ch, err := cm.startSlack(ctx, instanceID, sl)
		if err != nil {
			cm.logger.Warn("failed to start slack channel", "instance", instanceID, "error", err)
		} else if ch != nil {
			cm.trackChannel(instanceID, ch)
		}
	}

	// Ensure notification pump is running for instances with channels.
	cm.mu.Lock()
	hasChannels := len(cm.channels[instanceID]) > 0
	cm.mu.Unlock()
	if hasChannels {
		cm.router.EnsureNotificationPump(instanceID)
	}

	return nil
}

// trackChannel adds a successfully started channel to the instance's tracked set.
// Each channel is tracked immediately so OnInstanceStop can clean it up even if
// a subsequent channel fails to start.
func (cm *channelManager) trackChannel(instanceID string, ch channel.Channel) {
	cm.mu.Lock()
	cm.channels[instanceID] = append(cm.channels[instanceID], ch)
	cm.mu.Unlock()
}

// OnInstanceStop stops and unregisters all channels for the given instance.
func (cm *channelManager) OnInstanceStop(instanceID string) {
	cm.mu.Lock()
	channels := cm.channels[instanceID]
	delete(cm.channels, instanceID)
	cm.mu.Unlock()

	for _, ch := range channels {
		if err := ch.Stop(); err != nil {
			cm.logger.Warn("failed to stop channel", "channel", ch.Name(), "instance", instanceID, "error", err)
		}
		cm.router.Unregister(ch.Name())
		cm.logger.Info("channel stopped", "channel", ch.Name(), "instance", instanceID)
	}
}

func (cm *channelManager) startTelegram(ctx context.Context, instanceID string, cfg *config.InstanceTelegramConfig) (channel.Channel, error) {
	token := cm.cp.ResolveSecret(cfg.BotToken)
	if token == "" {
		return nil, fmt.Errorf("bot_token is empty or secret not found")
	}

	channelName := "telegram:" + instanceID
	tg := telegramchannel.New(telegramchannel.Config{
		Name:     channelName,
		Token:    token,
		Instance: instanceID,
	}, cm.router, cm.logger)
	cm.router.Register(tg)

	if err := tg.Start(ctx); err != nil {
		cm.router.Unregister(channelName)
		return nil, err
	}
	cm.logger.Info("telegram channel started", "instance", instanceID)
	return tg, nil
}

func (cm *channelManager) startSlack(ctx context.Context, instanceID string, cfg *config.InstanceSlackConfig) (channel.Channel, error) {
	botToken := cm.cp.ResolveSecret(cfg.BotToken)
	signingSecret := cm.cp.ResolveSecret(cfg.SigningSecret)
	if botToken == "" || signingSecret == "" {
		return nil, fmt.Errorf("bot_token or signing_secret is empty or secret not found")
	}

	channelName := "slack:" + instanceID
	routePattern := fmt.Sprintf("POST /api/instances/%s/slack/events", instanceID)

	sc := slackchannel.New(slackchannel.Config{
		Name:          channelName,
		BotToken:      botToken,
		SigningSecret: signingSecret,
		Instance:      instanceID,
		Mux:           cm.mux,
		RoutePattern:  routePattern,
	}, cm.router, cm.logger)
	cm.router.Register(sc)

	if err := sc.Start(ctx); err != nil {
		cm.router.Unregister(channelName)
		return nil, err
	}
	cm.logger.Info("slack channel started", "instance", instanceID, "route", routePattern)
	return sc, nil
}
