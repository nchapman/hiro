package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// --- Mocks ---

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

func (m *mockManager) InstanceNotifications(string) *inference.NotificationQueue { return nil }
func (m *mockManager) SessionIDForChannel(string, string) string                 { return "" }

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

// mockSlackAPI creates an httptest server that simulates the Slack Web API.
func mockSlackAPI(t *testing.T, postMessageFn func(channelID, text, threadTS string) error) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		body, _ := io.ReadAll(r.Body)
		var params map[string]string
		_ = json.Unmarshal(body, &params)

		if postMessageFn != nil {
			if err := postMessageFn(params["channel"], params["text"], params["thread_ts"]); err != nil {
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()}) //nolint:errcheck
				return
			}
		}

		json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234567890.123456"}) //nolint:errcheck
	}))
}

func testRouter(t *testing.T, mgr *mockManager) *channel.Router {
	t.Helper()
	return channel.NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
}

// waitFor polls until condition returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// signRequest adds valid Slack signature headers to a request body.
func signRequest(signingSecret string, body []byte) (timestamp, signature string) {
	timestamp = fmt.Sprintf("%d", time.Now().Unix())
	baseString := signatureVersion + ":" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(baseString))
	signature = signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	return timestamp, signature
}

// makeEventRequest constructs an HTTP request with a signed Slack event payload.
func makeEventRequest(t *testing.T, url, signingSecret string, payload any) *http.Request {
	t.Helper()
	body, _ := json.Marshal(payload)
	ts, sig := signRequest(signingSecret, body)

	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// --- Tests ---

func TestNameAndTrusted(t *testing.T) {
	t.Parallel()

	ch := New(Config{}, nil, slog.Default())
	if ch.Name() != "slack" {
		t.Errorf("Name() = %q", ch.Name())
	}
	if ch.Trusted() {
		t.Error("Slack should not be trusted")
	}
}

func TestStartStop(t *testing.T) {
	t.Parallel()

	ch := New(Config{}, nil, slog.Default())
	if err := ch.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := ch.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestURLVerificationChallenge(t *testing.T) {
	t.Parallel()

	secret := "test-signing-secret"
	mux := http.NewServeMux()
	ch := New(Config{SigningSecret: secret, Mux: mux}, nil, slog.Default())
	_ = ch.Start(t.Context())

	payload := map[string]string{
		"type":      "url_verification",
		"challenge": "challenge-token-abc",
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "challenge-token-abc" {
		t.Errorf("body = %q, want %q", body, "challenge-token-abc")
	}
}

func TestSignatureVerification_Valid(t *testing.T) {
	t.Parallel()

	ch := New(Config{SigningSecret: "secret"}, nil, slog.Default())
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts, sig := signRequest("secret", body)

	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", ts)
	headers.Set("X-Slack-Signature", sig)

	if !ch.verifySignature(headers, body) {
		t.Error("expected valid signature")
	}
}

func TestSignatureVerification_Invalid(t *testing.T) {
	t.Parallel()

	ch := New(Config{SigningSecret: "secret"}, nil, slog.Default())
	body := []byte(`{"type":"url_verification"}`)

	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))
	headers.Set("X-Slack-Signature", "v0=invalid")

	if ch.verifySignature(headers, body) {
		t.Error("expected invalid signature")
	}
}

func TestSignatureVerification_MissingHeaders(t *testing.T) {
	t.Parallel()

	ch := New(Config{SigningSecret: "secret"}, nil, slog.Default())

	if ch.verifySignature(http.Header{}, nil) {
		t.Error("expected rejection with missing headers")
	}
}

func TestSignatureVerification_OldTimestamp(t *testing.T) {
	t.Parallel()

	ch := New(Config{SigningSecret: "secret"}, nil, slog.Default())
	body := []byte(`{}`)
	oldTS := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())

	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("v0:" + oldTS + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", oldTS)
	headers.Set("X-Slack-Signature", sig)

	if ch.verifySignature(headers, body) {
		t.Error("expected rejection for old timestamp")
	}
}

func TestHandleEvents_InvalidSignature_Returns401(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	ch := New(Config{SigningSecret: "real-secret", Mux: mux}, nil, slog.Default())
	_ = ch.Start(t.Context())

	body := `{"type":"url_verification","challenge":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/slack/events", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))
	req.Header.Set("X-Slack-Signature", "v0=bad")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestEventCallback_MessageDispatch(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	var sentText, sentThread string
	var sentMu sync.Mutex
	slackAPI := mockSlackAPI(t, func(channelID, text, threadTS string) error {
		sentMu.Lock()
		sentText = text
		sentThread = threadTS
		sentMu.Unlock()
		return nil
	})
	defer slackAPI.Close()

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	ch := New(Config{
		BotToken:      "xoxb-test",
		SigningSecret: secret,
		Instance:      "operator",
		APIURL:        slackAPI.URL,
		Mux:           mux,
	}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	payload := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type":    "message",
			"channel": "C12345",
			"user":    "U67890",
			"text":    "hello bot",
			"ts":      "1234567890.000001",
		},
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	waitFor(t, 2*time.Second, func() bool {
		sentMu.Lock()
		defer sentMu.Unlock()
		return sentText != ""
	})

	sentMu.Lock()
	defer sentMu.Unlock()
	if !strings.Contains(sentText, "response to: hello bot") {
		t.Errorf("sent text = %q, want agent response", sentText)
	}
	if sentThread != "1234567890.000001" {
		t.Errorf("thread_ts = %q, want message ts", sentThread)
	}
}

func TestEventCallback_ThreadReply(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	var sentThread string
	var sentMu sync.Mutex
	slackAPI := mockSlackAPI(t, func(_, _, threadTS string) error {
		sentMu.Lock()
		sentThread = threadTS
		sentMu.Unlock()
		return nil
	})
	defer slackAPI.Close()

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	ch := New(Config{
		BotToken:      "xoxb-test",
		SigningSecret: secret,
		Instance:      "operator",
		APIURL:        slackAPI.URL,
		Mux:           mux,
	}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	// Message is already in a thread — should reply in the same thread.
	payload := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type":      "message",
			"channel":   "C12345",
			"user":      "U67890",
			"text":      "follow up",
			"ts":        "1234567890.000002",
			"thread_ts": "1234567890.000001",
		},
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	waitFor(t, 2*time.Second, func() bool {
		sentMu.Lock()
		defer sentMu.Unlock()
		return sentThread != ""
	})

	sentMu.Lock()
	defer sentMu.Unlock()
	if sentThread != "1234567890.000001" {
		t.Errorf("thread_ts = %q, want original thread ts", sentThread)
	}
}

func TestEventCallback_IgnoresBotMessages(t *testing.T) {
	t.Parallel()

	var dispatched atomic.Bool
	mgr := newMockManager()
	mgr.sendMessageFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		dispatched.Store(true)
		return "", nil
	}

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	ch := New(Config{SigningSecret: secret, Instance: "operator", Mux: mux}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	// Bot message — should be ignored.
	payload := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type":    "message",
			"channel": "C12345",
			"bot_id":  "B12345",
			"text":    "I am a bot",
			"ts":      "1234567890.000001",
		},
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	time.Sleep(50 * time.Millisecond)
	if dispatched.Load() {
		t.Error("should not dispatch bot messages")
	}
}

func TestEventCallback_IgnoresSubtypes(t *testing.T) {
	t.Parallel()

	var dispatched atomic.Bool
	mgr := newMockManager()
	mgr.sendMessageFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		dispatched.Store(true)
		return "", nil
	}

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	ch := New(Config{SigningSecret: secret, Instance: "operator", Mux: mux}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	// Message edit (subtype) — should be ignored.
	payload := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type":    "message",
			"subtype": "message_changed",
			"channel": "C12345",
			"text":    "edited text",
			"ts":      "1234567890.000001",
		},
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	time.Sleep(50 * time.Millisecond)
	if dispatched.Load() {
		t.Error("should not dispatch message subtypes")
	}
}

func TestAccessChecker(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	var dispatchCount atomic.Int32
	var pendingReplies atomic.Int32
	slackAPI := mockSlackAPI(t, func(ch, text, ts string) error {
		if text == "Your message is awaiting approval." {
			pendingReplies.Add(1)
		} else {
			dispatchCount.Add(1)
		}
		return nil
	})
	defer slackAPI.Close()

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	router.SetAccessChecker(&mockAccessChecker{
		results: map[string]channel.AccessResult{
			"slack:C_BLOCKED": channel.AccessDeny,
			"slack:C_PENDING": channel.AccessPending,
			"slack:C_ALLOWED": channel.AccessAllow,
		},
	})
	ch := New(Config{
		BotToken:      "xoxb-test",
		SigningSecret: secret,
		Instance:      "operator",
		APIURL:        slackAPI.URL,
		Mux:           mux,
	}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	// Message from blocked channel.
	blocked := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type": "message", "channel": "C_BLOCKED", "user": "U1",
			"text": "hi", "ts": "1.1",
		},
	}
	req := makeEventRequest(t, "/api/slack/events", secret, blocked)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Message from pending channel.
	pending := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type": "message", "channel": "C_PENDING", "user": "U1",
			"text": "hi", "ts": "1.2",
		},
	}
	req = makeEventRequest(t, "/api/slack/events", secret, pending)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Message from allowed channel.
	allowed := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type": "message", "channel": "C_ALLOWED", "user": "U1",
			"text": "hi", "ts": "1.3",
		},
	}
	req = makeEventRequest(t, "/api/slack/events", secret, allowed)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	waitFor(t, 2*time.Second, func() bool {
		return dispatchCount.Load() >= 1 && pendingReplies.Load() >= 1
	})

	if n := dispatchCount.Load(); n != 1 {
		t.Errorf("sent %d dispatch messages, want 1 (only allowed channel)", n)
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

func TestDeliver_PostsToThread(t *testing.T) {
	t.Parallel()

	var sentChannel, sentThread, sentText string
	var sentMu sync.Mutex
	slackAPI := mockSlackAPI(t, func(ch, text, ts string) error {
		sentMu.Lock()
		sentChannel = ch
		sentText = text
		sentThread = ts
		sentMu.Unlock()
		return nil
	})
	defer slackAPI.Close()

	ch := New(Config{BotToken: "xoxb-test", APIURL: slackAPI.URL}, nil, slog.Default())

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "notification "},
		{Type: "delta", Content: "here"},
	}
	err := ch.Deliver(t.Context(), "slack:C12345:1234.5678", events, channel.TurnResult{})
	if err != nil {
		t.Fatal(err)
	}

	sentMu.Lock()
	defer sentMu.Unlock()
	if sentChannel != "C12345" {
		t.Errorf("channel = %q", sentChannel)
	}
	if sentThread != "1234.5678" {
		t.Errorf("thread_ts = %q", sentThread)
	}
	if sentText != "notification here" {
		t.Errorf("text = %q", sentText)
	}
}

func TestDeliver_EmptyEvents_NoSend(t *testing.T) {
	t.Parallel()

	var sendCalled atomic.Bool
	slackAPI := mockSlackAPI(t, func(string, string, string) error {
		sendCalled.Store(true)
		return nil
	})
	defer slackAPI.Close()

	ch := New(Config{BotToken: "xoxb-test", APIURL: slackAPI.URL}, nil, slog.Default())

	events := []ipc.ChatEvent{{Type: "tool_call", ToolName: "Bash"}}
	err := ch.Deliver(t.Context(), "slack:C12345:1.1", events, channel.TurnResult{})
	if err != nil {
		t.Fatal(err)
	}

	if sendCalled.Load() {
		t.Error("should not send when events produce no text")
	}
}

func TestDeliver_InvalidKey(t *testing.T) {
	t.Parallel()

	ch := New(Config{BotToken: "xoxb-test"}, nil, slog.Default())
	err := ch.Deliver(t.Context(), "invalid:key", nil, channel.TurnResult{})
	if err == nil {
		t.Error("expected error for invalid conversation key")
	}
}

func TestConversationKey(t *testing.T) {
	t.Parallel()

	// With thread.
	key := buildConversationKey("C12345", "1234.5678")
	if key != "slack:C12345:1234.5678" {
		t.Errorf("key = %q", key)
	}
	ch, ts := parseConversationKey(key)
	if ch != "C12345" || ts != "1234.5678" {
		t.Errorf("parse = (%q, %q)", ch, ts)
	}

	// Without thread.
	key = buildConversationKey("C12345", "")
	if key != "slack:C12345" {
		t.Errorf("key = %q", key)
	}
	ch, ts = parseConversationKey(key)
	if ch != "C12345" || ts != "" {
		t.Errorf("parse = (%q, %q)", ch, ts)
	}

	// Invalid.
	ch, ts = parseConversationKey("tg:123")
	if ch != "" || ts != "" {
		t.Errorf("parse invalid = (%q, %q)", ch, ts)
	}
}

func TestFormatEvents(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "hello "},
		{Type: "tool_call", ToolName: "Bash"},
		{Type: "delta", Content: "world"},
	}
	text := channel.FormatEvents(events)
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
}

func TestFormatEvents_WithError(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "partial"},
		{Type: "error", Content: "failed"},
	}
	text := channel.FormatEvents(events)
	if !strings.Contains(text, "partial") || !strings.Contains(text, "Error: failed") {
		t.Errorf("text = %q", text)
	}
}

func TestUnresolvableInstance(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	// No instances — resolution will fail.

	var sentText atomic.Value
	slackAPI := mockSlackAPI(t, func(_, text, _ string) error {
		sentText.Store(text)
		return nil
	})
	defer slackAPI.Close()

	secret := "test-secret"
	mux := http.NewServeMux()
	router := testRouter(t, mgr)
	ch := New(Config{
		BotToken:      "xoxb-test",
		SigningSecret: secret,
		Instance:      "nonexistent",
		APIURL:        slackAPI.URL,
		Mux:           mux,
	}, router, slog.Default())
	router.Register(ch)
	_ = ch.Start(t.Context())

	payload := map[string]any{
		"type": "event_callback",
		"event": map[string]any{
			"type": "message", "channel": "C1", "user": "U1",
			"text": "hello", "ts": "1.1",
		},
	}

	req := makeEventRequest(t, "/api/slack/events", secret, payload)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	waitFor(t, 2*time.Second, func() bool {
		v, ok := sentText.Load().(string)
		return ok && v != ""
	})

	if v, _ := sentText.Load().(string); v != "Agent is not available." {
		t.Errorf("sent = %q, want unavailable message", v)
	}
}

func TestPostMessage_APIError(t *testing.T) {
	t.Parallel()

	slackAPI := mockSlackAPI(t, func(_, _, _ string) error {
		return fmt.Errorf("channel_not_found")
	})
	defer slackAPI.Close()

	ch := New(Config{BotToken: "xoxb-test", APIURL: slackAPI.URL}, nil, slog.Default())

	events := []ipc.ChatEvent{{Type: "delta", Content: "hello"}}
	err := ch.Deliver(t.Context(), "slack:C12345:1.1", events, channel.TurnResult{})
	if err == nil {
		t.Fatal("expected error from postMessage")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error = %v, want channel_not_found", err)
	}
}
