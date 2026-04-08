// Package slack implements the Slack messaging channel. It receives events
// via the Slack Events API (HTTP webhooks) and posts responses using the
// Web API (chat.postMessage). Bot replies are always posted in threads.
package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/ipc"
)

const (
	// slackAPIURL is the base URL for Slack Web API.
	slackAPIURL = "https://slack.com/api"

	// maxRequestBody is the maximum size of an incoming Slack event (1 MB).
	maxRequestBody = 1 << 20

	// signatureVersion is the Slack signature version prefix.
	signatureVersion = "v0"

	// signatureMaxAge is how old a request timestamp can be before rejecting it.
	signatureMaxAge = 5 * time.Minute

	// httpClientTimeout is the timeout for outbound Slack API calls.
	httpClientTimeout = 30 * time.Second
)

// Channel is the Slack messaging channel.
type Channel struct {
	name          string // channel name (default: "slack")
	botToken      string
	signingSecret string
	instance      string // agent name or instance ID to bind to
	router        *channel.Router
	apiURL        string // Slack API base URL (overridable for tests)
	client        *http.Client
	mux           *http.ServeMux // HTTP mux to register webhook routes on
	routePattern  string         // HTTP route for webhook events
	logger        *slog.Logger
}

// Config holds the configuration for a Slack channel.
type Config struct {
	Name          string         // channel name (default: "slack"); use "slack:<instanceID>" for per-instance channels
	BotToken      string         // bot OAuth token (already resolved)
	SigningSecret string         // signing secret (already resolved)
	Instance      string         // agent name or instance ID
	APIURL        string         // override for testing (default: https://slack.com/api)
	Mux           *http.ServeMux // HTTP mux to register routes on
	RoutePattern  string         // HTTP route pattern (default: "POST /api/slack/events")
}

// New creates a new Slack channel.
func New(cfg Config, router *channel.Router, logger *slog.Logger) *Channel {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = slackAPIURL
	}

	name := cfg.Name
	if name == "" {
		name = "slack"
	}

	routePattern := cfg.RoutePattern
	if routePattern == "" {
		routePattern = "POST /api/slack/events"
	}

	return &Channel{
		name:          name,
		botToken:      cfg.BotToken,
		signingSecret: cfg.SigningSecret,
		instance:      cfg.Instance,
		router:        router,
		apiURL:        apiURL,
		client:        &http.Client{Timeout: httpClientTimeout},
		mux:           cfg.Mux,
		routePattern:  routePattern,
		logger:        logger.With("channel", name),
	}
}

// Name returns the channel name (default "slack", or a custom name for per-instance channels).
func (c *Channel) Name() string { return c.name }

// Trusted returns false — external channels cannot run sensitive commands.
func (c *Channel) Trusted() bool { return false }

// Start registers the webhook HTTP routes. The HTTP server is managed externally.
func (c *Channel) Start(_ context.Context) error {
	if c.mux != nil {
		c.mux.HandleFunc(c.routePattern, c.handleEvents)
	}
	c.logger.Info("starting slack channel", "instance", c.instance, "route", c.routePattern)
	return nil
}

// Stop is a no-op — the HTTP server lifecycle is managed externally.
func (c *Channel) Stop() error { return nil }

// Deliver pushes notification events to a Slack thread as a formatted message.
func (c *Channel) Deliver(ctx context.Context, conversationKey string, events []ipc.ChatEvent, _ channel.TurnResult) error {
	channelID, threadTS := parseConversationKey(conversationKey)
	if channelID == "" {
		return fmt.Errorf("invalid slack conversation key: %q", conversationKey)
	}

	text := channel.FormatEvents(events)
	if text == "" {
		return nil
	}

	return c.postMessage(ctx, channelID, threadTS, text)
}

// --- HTTP Handlers ---

// handleEvents is the HTTP handler for Slack Events API webhooks.
func (c *Channel) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Verify request signature.
	if !c.verifySignature(r.Header, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse the outer envelope to determine the event type.
	var envelope struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	switch envelope.Type {
	case "url_verification":
		// Slack sends this during app setup to verify the endpoint.
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, envelope.Challenge)
		return

	case "event_callback":
		// Dispatch asynchronously — Slack requires a 200 response within 3 seconds.
		// Use a background context since r.Context() is cancelled when the handler returns.
		go c.handleEventCallback(context.Background(), body)
		w.WriteHeader(http.StatusOK)
		return

	default:
		w.WriteHeader(http.StatusOK)
	}
}

// handleEventCallback processes a Slack event_callback envelope.
func (c *Channel) handleEventCallback(ctx context.Context, body []byte) {
	var payload struct {
		Event slackEvent `json:"event"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		c.logger.Warn("failed to parse event callback", "error", err)
		return
	}

	evt := payload.Event

	// Only handle user messages (not bot messages, not subtypes like edits).
	if evt.Type != "message" || evt.SubType != "" || evt.BotID != "" {
		return
	}

	if evt.Text == "" {
		return
	}

	// Access check: approve/block/pending.
	if ac := c.router.AccessChecker(); ac != nil {
		senderKey := "slack:" + evt.Channel
		const sampleLen = 100
		sample := evt.Text
		if len(sample) > sampleLen {
			sample = sample[:sampleLen]
		}

		// Slack events don't include the channel name, so we use the channel ID
		// as the display name. The operator can identify it in their Slack workspace.
		result := ac.CheckAccess(c.instance, senderKey, evt.Channel, sample)
		switch result {
		case channel.AccessDeny:
			c.logger.Debug("blocked message from blocked channel",
				"channel", evt.Channel,
				"user", evt.User,
			)
			return
		case channel.AccessPending:
			c.logger.Info("channel pending approval",
				"channel", evt.Channel,
				"user", evt.User,
			)
			threadTS := evt.ThreadTS
			if threadTS == "" {
				threadTS = evt.TS
			}
			_ = c.postMessage(ctx, evt.Channel, threadTS, "Your message is awaiting approval.")
			return
		}
	}

	c.logger.Info("received message",
		"channel", evt.Channel,
		"user", evt.User,
		"text_length", len(evt.Text),
		"thread_ts", evt.ThreadTS,
	)

	c.dispatchMessage(ctx, evt)
}

// dispatchMessage resolves the instance and dispatches a Slack message through the Router.
func (c *Channel) dispatchMessage(ctx context.Context, evt slackEvent) { //nolint:funlen // session setup adds necessary error handling
	// Thread routing: if the message is in a thread, use the thread_ts.
	// If it is a top-level message, use the message's own ts as the thread
	// root (bot will reply in a new thread).
	threadTS := evt.ThreadTS
	if threadTS == "" {
		threadTS = evt.TS
	}
	conversationKey := buildConversationKey(evt.Channel, threadTS)

	b := c.router.Bind(conversationKey, "slack", c.instance)
	instanceID := b.ResolveInstanceID(c.router.Manager())
	if instanceID == "" {
		c.logger.Warn("could not resolve instance",
			"channel", evt.Channel,
			"target", c.instance,
		)
		_ = c.postMessage(ctx, evt.Channel, threadTS, "Agent is not available.")
		return
	}

	// Ensure a per-thread session exists for this Slack conversation.
	channelKey := "slack:" + evt.Channel
	if threadTS != "" {
		channelKey += ":" + threadTS
	}
	sessionID, err := c.router.Manager().EnsureSession(ctx, instanceID, channelKey)
	if err != nil {
		c.logger.Warn("failed to ensure slack session",
			"channel", evt.Channel,
			"instance_id", instanceID,
			"error", err,
		)
		_ = c.postMessage(ctx, evt.Channel, threadTS, "Agent is temporarily unavailable. Please try again later.")
		return
	}
	b.SessionID = sessionID

	// Ensure notifications for this instance are pumped to all channels.
	c.router.EnsureNotificationPump(instanceID)

	// Build buffering callbacks.
	var buf strings.Builder
	onEvent := channel.MakeBufferingOnEvent(&buf)

	onDone := func(_ channel.TurnResult) error {
		resp := buf.String()
		if resp == "" {
			resp = "(no response)"
		}
		return c.postMessage(ctx, evt.Channel, threadTS, resp)
	}

	if dispErr := c.router.Dispatch(ctx, channel.InboundMessage{
		ConversationKey: conversationKey,
		InstanceID:      instanceID,
		SessionID:       sessionID,
		ChannelName:     "slack",
		ChannelKey:      channelKey,
		Text:            evt.Text,
		OnEvent:         onEvent,
		OnDone:          onDone,
	}); dispErr != nil {
		c.logger.Warn("dispatch failed",
			"channel", evt.Channel,
			"error", dispErr,
		)
	}
}

// --- Slack Web API ---

// postMessage sends a message to a Slack channel, optionally in a thread.
func (c *Channel) postMessage(ctx context.Context, channelID, threadTS, text string) error {
	payload := map[string]string{
		"channel": channelID,
		"text":    text,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal chat.postMessage: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("chat.postMessage: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody))
	if err != nil {
		return fmt.Errorf("chat.postMessage: read response: %w", err)
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("chat.postMessage: parse response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return nil
}

// --- Signature Verification ---

// verifySignature validates the Slack request signature using HMAC-SHA256.
// See: https://api.slack.com/authentication/verifying-requests-from-slack
func (c *Channel) verifySignature(headers http.Header, body []byte) bool {
	timestamp := headers.Get("X-Slack-Request-Timestamp")
	signature := headers.Get("X-Slack-Signature")

	if timestamp == "" || signature == "" {
		return false
	}

	// Reject stale requests to prevent replay attacks. Allow a small
	// future tolerance (60s) for clock skew but don't accept requests
	// from further in the future.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if now-ts > int64(signatureMaxAge.Seconds()) || ts-now > 60 {
		return false
	}

	// Compute expected signature: v0=HMAC-SHA256(signing_secret, "v0:<timestamp>:<body>")
	baseString := signatureVersion + ":" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(c.signingSecret))
	mac.Write([]byte(baseString))
	expected := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// --- Types ---

// slackEvent represents a message event from the Slack Events API.
type slackEvent struct {
	Type     string `json:"type"`
	SubType  string `json:"subtype"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	BotID    string `json:"bot_id"`
}

// --- Helpers ---

// buildConversationKey creates a conversation key for a Slack channel+thread.
func buildConversationKey(channelID, threadTS string) string {
	if threadTS != "" {
		return "slack:" + channelID + ":" + threadTS
	}
	return "slack:" + channelID
}

// parseConversationKey extracts channel ID and optional thread_ts from a key.
func parseConversationKey(key string) (channelID, threadTS string) {
	if !strings.HasPrefix(key, "slack:") {
		return "", ""
	}
	rest := key[len("slack:"):]
	parts := strings.SplitN(rest, ":", 2) //nolint:mnd // split into at most 2 parts: channelID and optional threadTS
	channelID = parts[0]
	if len(parts) > 1 {
		threadTS = parts[1]
	}
	return channelID, threadTS
}
