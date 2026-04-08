package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
)

// --- Test Helpers ---

// mockTelegramAPI creates an httptest server that simulates the Telegram Bot API.
// getUpdatesFn handles getUpdates calls; sendMessageFn handles sendMessage calls.
func mockTelegramAPI(t *testing.T, getUpdatesFn func(offset int64) []update, sendMessageFn func(chatID int64, text string) error) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip /bot<token>/ prefix to get method name.
		parts := strings.SplitN(r.URL.Path, "/", 3)
		if len(parts) < 3 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		method := parts[2]

		body, _ := io.ReadAll(r.Body)
		var params map[string]any
		_ = json.Unmarshal(body, &params)

		switch method {
		case "getUpdates":
			var offset int64
			if v, ok := params["offset"]; ok {
				if f, ok := v.(float64); ok {
					offset = int64(f)
				}
			}
			updates := getUpdatesFn(offset)
			writeJSON(w, map[string]any{"ok": true, "result": updates})

		case "sendMessage":
			chatIDFloat, _ := params["chat_id"].(float64)
			chatID := int64(chatIDFloat)
			text, _ := params["text"].(string)
			if sendMessageFn != nil {
				if err := sendMessageFn(chatID, text); err != nil {
					writeJSON(w, map[string]any{"ok": false, "description": err.Error()})
					return
				}
			}
			writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})

		default:
			http.Error(w, "unknown method: "+method, http.StatusNotFound)
		}
	}))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// testRouter creates a minimal Router with a mock manager for testing.
func testRouter(t *testing.T, mgr *mockManager) *channel.Router {
	t.Helper()
	return channel.NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
}

type mockManager struct {
	mu             sync.Mutex
	sendMessageFn  func(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	instances      map[string]agent.InstanceInfo
	agentInstances map[string]string
}

func newMockManager() *mockManager {
	return &mockManager{
		instances:      make(map[string]agent.InstanceInfo),
		agentInstances: make(map[string]string),
	}
}

func (m *mockManager) SendMessage(ctx context.Context, instanceID, msg string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, instanceID, msg, onEvent)
	}
	if onEvent != nil {
		_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "response to: " + msg})
	}
	return "response to: " + msg, nil
}

func (m *mockManager) SendMessageWithFiles(_ context.Context, instanceID, msg string, _ []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessage(context.Background(), instanceID, msg, onEvent)
}

// Satisfy channel.ManagerInterface
func (m *mockManager) SendMessageToSession(ctx context.Context, instanceID, _, msg string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessage(ctx, instanceID, msg, onEvent)
}
func (m *mockManager) SendMessageToSessionWithFiles(ctx context.Context, instanceID, _, msg string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessageWithFiles(ctx, instanceID, msg, files, onEvent)
}
func (m *mockManager) SendMetaMessage(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
	return "", nil
}
func (m *mockManager) SendMetaMessageToSession(_ context.Context, _, _, _ string, _ func(ipc.ChatEvent) error) (string, error) {
	return "", nil
}
func (m *mockManager) EnsureSession(_ context.Context, _, _ string) (string, error) {
	return "test-session", nil
}
func (m *mockManager) NewSessionForChannel(string, string) (string, error) { return "new", nil }
func (m *mockManager) UpdateInstanceConfig(context.Context, string, string, *string, []string, []string) error {
	return nil
}
func (m *mockManager) InstanceNotifications(string) *inference.NotificationQueue {
	return nil
}
func (m *mockManager) SessionIDForChannel(string, string) string { return "" }
func (m *mockManager) GetInstance(id string) (agent.InstanceInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.instances[id]
	return info, ok
}
func (m *mockManager) InstanceByAgentName(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.agentInstances[name]
	return id, ok
}

type mockCmdHandler struct{}

func (h *mockCmdHandler) HandleCommand(input string) (string, error) {
	return "ok: " + input, nil
}

// --- Tests ---

func TestSplitMessage(t *testing.T) {
	t.Parallel()

	t.Run("short message", func(t *testing.T) {
		t.Parallel()
		chunks := splitMessage("hello", 100)
		if len(chunks) != 1 || chunks[0] != "hello" {
			t.Errorf("got %v", chunks)
		}
	})

	t.Run("exact limit", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a", 100)
		chunks := splitMessage(text, 100)
		if len(chunks) != 1 {
			t.Errorf("got %d chunks, want 1", len(chunks))
		}
	})

	t.Run("split at newline", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a", 50) + "\n" + strings.Repeat("b", 50)
		chunks := splitMessage(text, 60)
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
		if !strings.HasSuffix(chunks[0], "\n") {
			t.Errorf("first chunk should end with newline: %q", chunks[0])
		}
		if chunks[1] != strings.Repeat("b", 50) {
			t.Errorf("second chunk = %q", chunks[1])
		}
	})

	t.Run("no newline split", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a", 150)
		chunks := splitMessage(text, 100)
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
		if len(chunks[0]) != 100 {
			t.Errorf("first chunk len = %d, want 100", len(chunks[0]))
		}
		if len(chunks[1]) != 50 {
			t.Errorf("second chunk len = %d, want 50", len(chunks[1]))
		}
	})

	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		chunks := splitMessage("", 100)
		if len(chunks) != 1 || chunks[0] != "" {
			t.Errorf("got %v", chunks)
		}
	})
}

func TestConversationKey(t *testing.T) {
	t.Parallel()

	key := conversationKeyFor(12345)
	if key != "tg:12345" {
		t.Errorf("key = %q, want %q", key, "tg:12345")
	}

	id, err := parseChatID(key)
	if err != nil {
		t.Fatal(err)
	}
	if id != 12345 {
		t.Errorf("id = %d, want 12345", id)
	}

	// Negative chat IDs (groups).
	key = conversationKeyFor(-100123)
	id, err = parseChatID(key)
	if err != nil {
		t.Fatal(err)
	}
	if id != -100123 {
		t.Errorf("id = %d, want -100123", id)
	}

	// Invalid key.
	_, err = parseChatID("invalid:123")
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestFormatEvents(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "hello "},
		{Type: "tool_call", ToolName: "Bash"}, // ignored
		{Type: "delta", Content: "world"},
		{Type: "reasoning_delta", Content: "thinking"}, // ignored
	}

	text := channel.FormatEvents(events)
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
}

func TestFormatEvents_WithError(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "partial"},
		{Type: "error", Content: "something failed"},
	}

	text := channel.FormatEvents(events)
	if !strings.Contains(text, "partial") || !strings.Contains(text, "something failed") {
		t.Errorf("text = %q", text)
	}
}

func TestAccessChecker(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	var sentMessages atomic.Int32
	var pendingReplies atomic.Int32
	srv := mockTelegramAPI(t, func(offset int64) []update {
		if offset == 0 {
			return []update{
				{UpdateID: 1, Message: &message{Chat: chat{ID: 999}, From: user{Username: "blocked"}, Text: "hi"}},
				{UpdateID: 2, Message: &message{Chat: chat{ID: 888}, From: user{Username: "unknown"}, Text: "hello"}},
				{UpdateID: 3, Message: &message{Chat: chat{ID: 100}, From: user{Username: "allowed"}, Text: "hello"}},
			}
		}
		return nil
	}, func(chatID int64, text string) error {
		if text == "Your message is awaiting approval." {
			pendingReplies.Add(1)
		} else {
			sentMessages.Add(1)
		}
		return nil
	})
	defer srv.Close()

	router := testRouter(t, mgr)
	router.SetAccessChecker(&mockAccessChecker{
		results: map[string]channel.AccessResult{
			"tg:999": channel.AccessDeny,
			"tg:888": channel.AccessPending,
			"tg:100": channel.AccessAllow,
		},
	})
	ch := New(Config{
		Token:       "test-token",
		Instance:    "operator",
		BaseURL:     srv.URL,
		PollTimeout: 1,
	}, router, slog.Default())
	router.Register(ch)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_ = ch.Start(ctx)

	time.Sleep(500 * time.Millisecond)

	// Blocked chat: no messages. Pending: 1 pending reply. Allowed: 1 dispatched.
	if n := sentMessages.Load(); n != 1 {
		t.Errorf("sent %d dispatch messages, want 1 (only allowed chat)", n)
	}
	if n := pendingReplies.Load(); n != 1 {
		t.Errorf("sent %d pending replies, want 1", n)
	}
}

type mockAccessChecker struct {
	results map[string]channel.AccessResult
}

func (m *mockAccessChecker) CheckAccess(_, senderKey, _, _ string) channel.AccessResult {
	if r, ok := m.results[senderKey]; ok {
		return r
	}
	return channel.AccessPending
}

func TestEndToEnd_MessageAndResponse(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	var capturedResponse string
	var responseMu sync.Mutex

	srv := mockTelegramAPI(t, func(offset int64) []update {
		if offset == 0 {
			return []update{
				{UpdateID: 1, Message: &message{
					Chat: chat{ID: 42},
					From: user{Username: "testuser"},
					Text: "what is 2+2?",
				}},
			}
		}
		return nil
	}, func(chatID int64, text string) error {
		responseMu.Lock()
		capturedResponse = text
		responseMu.Unlock()
		return nil
	})
	defer srv.Close()

	router := testRouter(t, mgr)
	ch := New(Config{
		Token:       "test-token",
		Instance:    "operator",
		BaseURL:     srv.URL,
		PollTimeout: 1,
	}, router, slog.Default())
	router.Register(ch)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_ = ch.Start(ctx)

	// Wait for the message to be processed.
	time.Sleep(500 * time.Millisecond)

	responseMu.Lock()
	resp := capturedResponse
	responseMu.Unlock()

	if !strings.Contains(resp, "response to: what is 2+2?") {
		t.Errorf("response = %q, expected agent response", resp)
	}
}

func TestDeliver_SendsFormattedMessage(t *testing.T) {
	t.Parallel()

	var sentText string
	var sentMu sync.Mutex

	srv := mockTelegramAPI(t, func(int64) []update { return nil }, func(chatID int64, text string) error {
		sentMu.Lock()
		sentText = text
		sentMu.Unlock()
		return nil
	})
	defer srv.Close()

	ch := New(Config{
		Token:   "test-token",
		BaseURL: srv.URL,
	}, nil, slog.Default()) // router not needed for Deliver

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "notification "},
		{Type: "delta", Content: "content"},
	}

	err := ch.Deliver(t.Context(), "tg:42", events, channel.TurnResult{})
	if err != nil {
		t.Fatal(err)
	}

	sentMu.Lock()
	defer sentMu.Unlock()
	if sentText != "notification content" {
		t.Errorf("sent = %q, want %q", sentText, "notification content")
	}
}

func TestDeliver_EmptyEventsNoSend(t *testing.T) {
	t.Parallel()

	var sendCalled atomic.Bool
	srv := mockTelegramAPI(t, func(int64) []update { return nil }, func(int64, string) error {
		sendCalled.Store(true)
		return nil
	})
	defer srv.Close()

	ch := New(Config{Token: "test-token", BaseURL: srv.URL}, nil, slog.Default())

	// Only tool_call events — no text content.
	events := []ipc.ChatEvent{
		{Type: "tool_call", ToolName: "Bash"},
		{Type: "reasoning_delta", Content: "thinking"},
	}

	err := ch.Deliver(t.Context(), "tg:42", events, channel.TurnResult{})
	if err != nil {
		t.Fatal(err)
	}

	if sendCalled.Load() {
		t.Error("should not send message when events produce no text")
	}
}

func TestDeliver_InvalidKey(t *testing.T) {
	t.Parallel()

	ch := New(Config{Token: "test-token"}, nil, slog.Default())
	err := ch.Deliver(t.Context(), "invalid:key", nil, channel.TurnResult{})
	if err == nil {
		t.Error("expected error for invalid conversation key")
	}
}

func TestNameAndTrusted(t *testing.T) {
	t.Parallel()

	ch := New(Config{}, nil, slog.Default())
	if ch.Name() != "telegram" {
		t.Errorf("Name() = %q", ch.Name())
	}
	if ch.Trusted() {
		t.Error("Telegram should not be trusted")
	}
}

func TestSendLong_SplitsMessages(t *testing.T) {
	t.Parallel()

	var sentMessages []string
	var mu sync.Mutex

	srv := mockTelegramAPI(t, func(int64) []update { return nil }, func(chatID int64, text string) error {
		mu.Lock()
		sentMessages = append(sentMessages, text)
		mu.Unlock()
		return nil
	})
	defer srv.Close()

	ch := New(Config{Token: "test-token", BaseURL: srv.URL}, nil, slog.Default())

	// Send a message longer than maxMessageLen.
	longText := strings.Repeat("a", maxMessageLen+100)
	err := ch.sendLong(t.Context(), 42, longText)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sentMessages) != 2 {
		t.Fatalf("sent %d messages, want 2", len(sentMessages))
	}
	totalLen := len(sentMessages[0]) + len(sentMessages[1])
	if totalLen != maxMessageLen+100 {
		t.Errorf("total length = %d, want %d", totalLen, maxMessageLen+100)
	}
}

func TestStop(t *testing.T) {
	t.Parallel()

	srv := mockTelegramAPI(t, func(int64) []update { return nil }, nil)
	defer srv.Close()

	ch := New(Config{
		Token:       "test-token",
		BaseURL:     srv.URL,
		PollTimeout: 1,
	}, nil, slog.Default())

	ctx, cancel := context.WithCancel(t.Context())
	_ = ch.Start(ctx)

	// Stop should be idempotent.
	if err := ch.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Stop(); err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestRetrySendPlain_ParseError(t *testing.T) {
	t.Parallel()

	callCount := 0
	srv := mockTelegramAPI(t, func(int64) []update { return nil }, func(chatID int64, text string) error {
		callCount++
		return nil
	})
	defer srv.Close()

	ch := New(Config{Token: "test-token", BaseURL: srv.URL}, nil, slog.Default())

	// Simulate a parse error — should retry without Markdown.
	params := map[string]any{
		"chat_id":    float64(42),
		"text":       "hello *world",
		"parse_mode": "Markdown",
	}
	err := ch.retrySendPlain(t.Context(), params, "Bad Request: can't parse entities in message text")
	if err != nil {
		t.Fatal(err)
	}
	// Verify parse_mode was cleared.
	if params["parse_mode"] != "" {
		t.Errorf("parse_mode should be cleared, got %q", params["parse_mode"])
	}
}

func TestRetrySendPlain_NonParseError(t *testing.T) {
	t.Parallel()

	ch := New(Config{Token: "test-token"}, nil, slog.Default())

	// Non-parse error — should return the error without retrying.
	err := ch.retrySendPlain(t.Context(), nil, "Too Many Requests: retry after 30")
	if err == nil {
		t.Fatal("expected error for non-parse failure")
	}
	if !strings.Contains(err.Error(), "Too Many Requests") {
		t.Errorf("error = %v", err)
	}
}

func TestHandleUpdate_EmptyText(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	router := testRouter(t, mgr)

	var sendCalled atomic.Bool
	mgr.sendMessageFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		sendCalled.Store(true)
		return "", nil
	}

	ch := New(Config{Token: "test-token", Instance: "operator"}, router, slog.Default())
	router.Register(ch)

	// Message with empty text should be ignored.
	ch.handleUpdate(t.Context(), update{
		UpdateID: 1,
		Message:  &message{Chat: chat{ID: 42}, Text: ""},
	})

	if sendCalled.Load() {
		t.Error("should not dispatch empty text message")
	}
}

func TestMakeBufferingOnEvent(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := channel.MakeBufferingOnEvent(&buf)

	// Delta events.
	_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "hello "})
	_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "world"})
	if buf.String() != "hello world" {
		t.Errorf("buf = %q", buf.String())
	}

	// Error after content — includes separator.
	_ = onEvent(ipc.ChatEvent{Type: "error", Content: "oops"})
	if !strings.Contains(buf.String(), "\n\nError: oops") {
		t.Errorf("buf = %q, want separator before error", buf.String())
	}

	// Error on empty buffer — no separator.
	var buf2 strings.Builder
	onEvent2 := channel.MakeBufferingOnEvent(&buf2)
	_ = onEvent2(ipc.ChatEvent{Type: "error", Content: "fail"})
	if buf2.String() != "Error: fail" {
		t.Errorf("buf = %q, want no separator", buf2.String())
	}

	// Ignored event types.
	var buf3 strings.Builder
	onEvent3 := channel.MakeBufferingOnEvent(&buf3)
	_ = onEvent3(ipc.ChatEvent{Type: "tool_call", ToolName: "Bash"})
	_ = onEvent3(ipc.ChatEvent{Type: "reasoning_delta", Content: "thinking"})
	if buf3.String() != "" {
		t.Errorf("buf = %q, want empty", buf3.String())
	}
}

func TestHandleUpdate_NilMessage(t *testing.T) {
	t.Parallel()

	ch := New(Config{Token: "test-token"}, nil, slog.Default())

	// Nil message should not panic.
	ch.handleUpdate(t.Context(), update{UpdateID: 1, Message: nil})
}

func TestHandleUpdate_UnresolvableInstance(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	// No instances registered — resolution will fail.
	router := testRouter(t, mgr)

	var sentText string
	srv := mockTelegramAPI(t, func(int64) []update { return nil }, func(chatID int64, text string) error {
		sentText = text
		return nil
	})
	defer srv.Close()

	ch := New(Config{
		Token:    "test-token",
		Instance: "nonexistent",
		BaseURL:  srv.URL,
	}, router, slog.Default())
	router.Register(ch)

	ch.handleUpdate(t.Context(), update{
		UpdateID: 1,
		Message:  &message{Chat: chat{ID: 42}, From: user{Username: "test"}, Text: "hello"},
	})

	if sentText != "Agent is not available." {
		t.Errorf("expected unavailable message, got %q", sentText)
	}
}
