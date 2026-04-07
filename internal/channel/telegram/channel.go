// Package telegram implements the Telegram messaging channel. It connects
// to the Telegram Bot API using long-polling (getUpdates) and dispatches
// inbound messages to agent instances via the Router.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/ipc"
)

const (
	// maxMessageLen is Telegram's maximum message length.
	maxMessageLen = 4096

	// pollTimeout is the long-poll timeout for getUpdates (seconds).
	pollTimeout = 30

	// maxRetryDelay caps the exponential backoff between retries.
	maxRetryDelay = 2 * time.Minute

	// baseRetryDelay is the initial backoff delay.
	baseRetryDelay = time.Second

	// pollTimeoutPadding is extra seconds added to HTTP client timeout beyond poll timeout.
	pollTimeoutPadding = 10

	// backoffMultiplier is the exponential backoff factor.
	backoffMultiplier = 2

	// apiResponseLimit is the maximum response body size from Telegram API (1 MB).
	apiResponseLimit = 1 << 20
)

// Channel is the Telegram messaging channel.
type Channel struct {
	name        string // channel name (default: "telegram")
	token       string
	instance    string // agent name or instance ID to bind to
	router      *channel.Router
	baseURL     string // Telegram API base URL (overridable for tests)
	pollTimeout int    // getUpdates timeout in seconds
	client      *http.Client
	logger      *slog.Logger

	stopOnce sync.Once
	stopCh   chan struct{}
}

// Config holds the configuration for a Telegram channel.
type Config struct {
	Name        string // channel name (default: "telegram"); use "telegram:<instanceID>" for per-instance channels
	Token       string // bot API token (already resolved from secret)
	Instance    string // agent name or instance ID
	BaseURL     string // override for testing (default: https://api.telegram.org)
	PollTimeout int    // override for testing (default: 30 seconds)
}

// New creates a new Telegram channel.
func New(cfg Config, router *channel.Router, logger *slog.Logger) *Channel {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}

	pt := cfg.PollTimeout
	if pt <= 0 {
		pt = pollTimeout
	}

	name := cfg.Name
	if name == "" {
		name = "telegram"
	}

	return &Channel{
		name:        name,
		token:       cfg.Token,
		instance:    cfg.Instance,
		router:      router,
		baseURL:     baseURL,
		pollTimeout: pt,
		client:      &http.Client{Timeout: time.Duration(pt+pollTimeoutPadding) * time.Second},
		logger:      logger.With("channel", name),
		stopCh:      make(chan struct{}),
	}
}

// Name returns the channel name (default "telegram", or a custom name for per-instance channels).
func (c *Channel) Name() string { return c.name }

// Trusted returns false — external channels cannot run sensitive commands.
func (c *Channel) Trusted() bool { return false }

// Start begins the long-poll loop for receiving Telegram updates.
func (c *Channel) Start(ctx context.Context) error {
	c.logger.Info("starting telegram channel", "instance", c.instance)
	go c.pollLoop(ctx)
	return nil
}

// Stop gracefully shuts down the channel.
func (c *Channel) Stop() error {
	c.stopOnce.Do(func() { close(c.stopCh) })
	return nil
}

// Deliver pushes notification events to a Telegram chat as a formatted message.
func (c *Channel) Deliver(ctx context.Context, conversationKey string, events []ipc.ChatEvent, _ channel.TurnResult) error {
	chatID, err := parseChatID(conversationKey)
	if err != nil {
		return err
	}

	text := channel.FormatEvents(events)
	if text == "" {
		return nil
	}

	return c.sendLong(ctx, chatID, text)
}

// --- Polling ---

// pollLoop runs the getUpdates long-poll loop with exponential backoff on errors.
func (c *Channel) pollLoop(ctx context.Context) {
	var offset int64
	delay := baseRetryDelay

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		default:
		}

		updates, err := c.getUpdates(ctx, offset)
		if err != nil {
			c.logger.Warn("getUpdates failed, retrying", "error", err, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			}
			delay = min(delay*backoffMultiplier, maxRetryDelay)
			continue
		}

		// Reset backoff on success.
		delay = baseRetryDelay

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			c.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate processes a single Telegram update.
func (c *Channel) handleUpdate(ctx context.Context, u update) {
	msg := u.Message
	if msg == nil || msg.Text == "" {
		return
	}

	// Access check: approve/block/pending.
	if ac := c.router.AccessChecker(); ac != nil {
		senderKey := conversationKeyFor(msg.Chat.ID)
		displayName := msg.Chat.Title
		if displayName == "" {
			displayName = msg.From.FirstName
			if msg.From.Username != "" {
				displayName += " (@" + msg.From.Username + ")"
			}
		}
		const sampleLen = 100
		sample := msg.Text
		if len(sample) > sampleLen {
			sample = sample[:sampleLen]
		}

		result := ac.CheckAccess(c.instance, senderKey, displayName, sample)
		switch result {
		case channel.AccessDeny:
			c.logger.Debug("blocked message from blocked sender",
				"chat_id", msg.Chat.ID,
				"user", msg.From.Username,
			)
			return
		case channel.AccessPending:
			c.logger.Info("sender pending approval",
				"chat_id", msg.Chat.ID,
				"user", msg.From.Username,
			)
			_ = c.sendMessage(ctx, msg.Chat.ID, "Your message is awaiting approval from the operator.")
			return
		}
	}

	conversationKey := conversationKeyFor(msg.Chat.ID)
	b := c.router.Bind(conversationKey, "telegram", c.instance)

	c.logger.Info("received message",
		"chat_id", msg.Chat.ID,
		"user", msg.From.Username,
		"text_length", len(msg.Text),
	)

	c.dispatchMessage(ctx, msg.Chat.ID, conversationKey, b, msg.Text, strconv.FormatInt(msg.Chat.ID, 10))
}

// dispatchMessage resolves the instance and dispatches a message through the Router.
func (c *Channel) dispatchMessage(ctx context.Context, chatID int64, conversationKey string, b *channel.Binding, text, chatIDStr string) {
	instanceID := b.ResolveInstanceID(c.router.Manager())
	if instanceID == "" {
		c.logger.Warn("could not resolve instance", "chat_id", chatID, "target", c.instance)
		_ = c.sendMessage(ctx, chatID, "Agent is not available.")
		return
	}

	// Ensure a per-chat session exists for this Telegram conversation.
	channelKey := "tg:" + chatIDStr
	sessionID, err := c.router.Manager().EnsureSession(ctx, instanceID, channelKey)
	if err != nil {
		c.logger.Warn("failed to ensure telegram session",
			"chat_id", chatID,
			"instance_id", instanceID,
			"error", err,
		)
		_ = c.sendMessage(ctx, chatID, "Agent is temporarily unavailable. Please try again later.")
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
		return c.sendLong(ctx, chatID, resp)
	}

	if dispErr := c.router.Dispatch(ctx, channel.InboundMessage{
		ConversationKey: conversationKey,
		InstanceID:      instanceID,
		SessionID:       sessionID,
		ChannelName:     "telegram",
		ChannelKey:      channelKey,
		Text:            text,
		OnEvent:         onEvent,
		OnDone:          onDone,
	}); dispErr != nil {
		c.logger.Warn("dispatch failed", "chat_id", chatID, "error", dispErr)
	}
}

// --- Bot API ---

// getUpdates calls the Telegram getUpdates endpoint with long-polling.
func (c *Channel) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	params := map[string]any{
		"offset":  offset,
		"timeout": c.pollTimeout,
	}

	var result struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
		Desc   string   `json:"description"`
	}

	if err := c.apiCall(ctx, "getUpdates", params, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("getUpdates: %s", result.Desc)
	}
	return result.Result, nil
}

// sendMessage sends a text message to a chat. Returns the sent message ID.
func (c *Channel) sendMessage(ctx context.Context, chatID int64, text string) error {
	params := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	var result struct {
		OK   bool   `json:"ok"`
		Desc string `json:"description"`
	}

	if err := c.apiCall(ctx, "sendMessage", params, &result); err != nil {
		return err
	}
	if !result.OK {
		return c.retrySendPlain(ctx, params, result.Desc)
	}
	return nil
}

// retrySendPlain retries a failed sendMessage without Markdown parse mode.
// Returns the original error if the failure is not a Markdown parse error.
// Telegram's documented error for malformed Markdown is:
// "Bad Request: can't parse entities in message text"
func (c *Channel) retrySendPlain(ctx context.Context, params map[string]any, desc string) error {
	lower := strings.ToLower(desc)
	if !strings.Contains(lower, "entities") && !strings.Contains(lower, "parse") {
		return fmt.Errorf("sendMessage: %s", desc)
	}
	params["parse_mode"] = ""
	var result struct {
		OK   bool   `json:"ok"`
		Desc string `json:"description"`
	}
	if err := c.apiCall(ctx, "sendMessage", params, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("sendMessage: %s", result.Desc)
	}
	return nil
}

// sendLong sends a potentially long message, splitting into chunks if needed.
func (c *Channel) sendLong(ctx context.Context, chatID int64, text string) error {
	chunks := splitMessage(text, maxMessageLen)
	for _, chunk := range chunks {
		if err := c.sendMessage(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// apiCall makes a JSON POST request to a Telegram Bot API method.
func (c *Channel) apiCall(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal %s params: %w", method, err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: failed to build request", method)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// Don't wrap the raw error — it may contain the full URL which
		// includes the bot token. Report the context error if available
		// (timeout, cancellation), otherwise a generic message.
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", method, ctx.Err())
		}
		return fmt.Errorf("%s: request failed", method)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, apiResponseLimit))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", method, err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, string(respBody))
	}

	return json.Unmarshal(respBody, result)
}

// --- Types ---

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID int64  `json:"message_id"`
	From      user   `json:"from"`
	Chat      chat   `json:"chat"`
	Text      string `json:"text"`
}

type user struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "private", "group", "supergroup", "channel"
	Title string `json:"title"`
}

// --- Helpers ---

// conversationKeyFor builds a conversation key from a Telegram chat ID.
func conversationKeyFor(chatID int64) string {
	return "tg:" + strconv.FormatInt(chatID, 10)
}

// parseChatID extracts the chat ID from a conversation key.
func parseChatID(key string) (int64, error) {
	if !strings.HasPrefix(key, "tg:") {
		return 0, fmt.Errorf("invalid telegram conversation key: %q", key)
	}
	return strconv.ParseInt(key[3:], 10, 64)
}

// splitMessage splits text into chunks of at most maxLen characters,
// preferring to split at newlines.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for text != "" {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split at a newline within the limit.
		cut := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > 0 {
			cut = idx + 1 // include the newline
		}

		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}
