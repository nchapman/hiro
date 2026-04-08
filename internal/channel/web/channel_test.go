package web

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	ws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
)

// --- Mock Manager for web channel tests ---

type mockManager struct {
	sendMessageFn   func(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	updateConfigFn  func(ctx context.Context, instanceID, model string, re *string) error
	ensureSessionFn func(ctx context.Context, instanceID, channelKey string) (string, error)
	instances       map[string]agent.InstanceInfo
	agentInstances  map[string]string
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
		_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "reply"})
	}
	return "reply", nil
}

func (m *mockManager) SendMessageWithFiles(ctx context.Context, instanceID, msg string, _ []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessage(ctx, instanceID, msg, onEvent)
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
func (m *mockManager) EnsureSession(ctx context.Context, instanceID, channelKey string) (string, error) {
	if m.ensureSessionFn != nil {
		return m.ensureSessionFn(ctx, instanceID, channelKey)
	}
	return "test-session", nil
}
func (m *mockManager) NewSessionForChannel(string, string) (string, error) { return "new", nil }

func (m *mockManager) UpdateInstanceConfig(ctx context.Context, instanceID, model string, re *string, _, _ []string) error {
	if m.updateConfigFn != nil {
		return m.updateConfigFn(ctx, instanceID, model, re)
	}
	return nil
}

func (m *mockManager) InstanceNotifications(string) *inference.NotificationQueue { return nil }
func (m *mockManager) SessionIDForChannel(string, string) string                 { return "" }

func (m *mockManager) GetInstance(id string) (agent.InstanceInfo, bool) {
	info, ok := m.instances[id]
	return info, ok
}

func (m *mockManager) InstanceByAgentName(name string) (string, bool) {
	id, ok := m.agentInstances[name]
	return id, ok
}

type mockCmdHandler struct{}

func (h *mockCmdHandler) HandleCommand(input string) (string, error) {
	return "ok: " + input, nil
}

// setupWebChannel creates a test HTTP server with the web channel wired up.
// Returns the server, the channel, and a cleanup function.
func setupWebChannel(t *testing.T, mgr *mockManager) (*httptest.Server, *Channel) {
	t.Helper()

	router := channel.NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
	wc := New(router, mgr, slog.Default())
	router.Register(wc)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Accept(w, r, nil)
		if err != nil {
			t.Logf("ws accept error: %v", err)
			return
		}
		defer conn.Close(ws.StatusNormalClosure, "")

		wc.HandleConn(r.Context(), conn, "inst-1", "web:test-conn")
	}))

	t.Cleanup(srv.Close)
	return srv, wc
}

func dialWS(t *testing.T, srv *httptest.Server) *ws.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := ws.Dial(t.Context(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return conn
}

func readMessage(t *testing.T, conn *ws.Conn) ChatMessage {
	t.Helper()
	var msg ChatMessage
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg
}

// consumeSessionEvent reads and discards the initial "session" event sent
// on every new WebSocket connection.
func consumeSessionEvent(t *testing.T, conn *ws.Conn) {
	t.Helper()
	msg := readMessage(t, conn)
	if msg.Type != "session" {
		t.Fatalf("expected initial session event, got %q", msg.Type)
	}
}

// --- WebSocket Integration Tests ---

func TestHandleConn_MessageAndResponse(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.sendMessageFn = func(_ context.Context, _ string, msg string, onEvent func(ipc.ChatEvent) error) (string, error) {
		_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "hello back"})
		return "hello back", nil
	}

	srv, _ := setupWebChannel(t, mgr)
	conn := dialWS(t, srv)
	defer conn.Close(ws.StatusNormalClosure, "")

	consumeSessionEvent(t, conn)

	// Send a message.
	_ = wsjson.Write(t.Context(), conn, ChatMessage{Type: "message", Content: "hello"})

	// Read the delta event.
	msg := readMessage(t, conn)
	if msg.Type != "delta" || msg.Content != "hello back" {
		t.Errorf("got %+v, want delta 'hello back'", msg)
	}

	// Read the done event.
	msg = readMessage(t, conn)
	if msg.Type != "done" {
		t.Errorf("got type %q, want 'done'", msg.Type)
	}
}

func TestHandleConn_SlashClear(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}

	srv, _ := setupWebChannel(t, mgr)
	conn := dialWS(t, srv)
	defer conn.Close(ws.StatusNormalClosure, "")

	consumeSessionEvent(t, conn)
	_ = wsjson.Write(t.Context(), conn, ChatMessage{Type: "message", Content: "/clear"})

	// Should get clear + done.
	msg := readMessage(t, conn)
	if msg.Type != "clear" {
		t.Errorf("got type %q, want 'clear'", msg.Type)
	}
	msg = readMessage(t, conn)
	if msg.Type != "done" {
		t.Errorf("got type %q, want 'done'", msg.Type)
	}
}

func TestHandleConn_ConfigMessage(t *testing.T) {
	t.Parallel()

	var configuredModel string
	var configMu sync.Mutex
	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.updateConfigFn = func(_ context.Context, _ string, model string, _ *string) error {
		configMu.Lock()
		configuredModel = model
		configMu.Unlock()
		return nil
	}

	srv, _ := setupWebChannel(t, mgr)
	conn := dialWS(t, srv)
	defer conn.Close(ws.StatusNormalClosure, "")

	consumeSessionEvent(t, conn)
	_ = wsjson.Write(t.Context(), conn, ChatMessage{Type: "config", Model: "claude-4"})

	// Should get system + done.
	msg := readMessage(t, conn)
	if msg.Type != "system" || !strings.Contains(msg.Content, "Configuration updated") {
		t.Errorf("got %+v, want system config confirmation", msg)
	}
	msg = readMessage(t, conn)
	if msg.Type != "done" {
		t.Errorf("got type %q, want 'done'", msg.Type)
	}

	configMu.Lock()
	defer configMu.Unlock()
	if configuredModel != "claude-4" {
		t.Errorf("model = %q, want %q", configuredModel, "claude-4")
	}
}

func TestDeliver_StreamsEventsToWebSocket(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}

	srv, wc := setupWebChannel(t, mgr)
	conn := dialWS(t, srv)
	defer conn.Close(ws.StatusNormalClosure, "")

	consumeSessionEvent(t, conn)

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "notification text"},
	}
	err := wc.Deliver(t.Context(), "web:test-conn", events, channel.TurnResult{})
	if err != nil {
		t.Fatal(err)
	}

	msg := readMessage(t, conn)
	if msg.Type != "delta" || msg.Content != "notification text" {
		t.Errorf("got %+v, want delta notification", msg)
	}

	msg = readMessage(t, conn)
	if msg.Type != "done" {
		t.Errorf("got type %q, want 'done'", msg.Type)
	}
}

func TestDeliver_NoConnection_ReturnsChannelClosed(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	router := channel.NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
	wc := New(router, mgr, slog.Default())

	err := wc.Deliver(t.Context(), "web:nonexistent", nil, channel.TurnResult{})
	if !errors.Is(err, channel.ErrChannelClosed) {
		t.Errorf("err = %v, want ErrChannelClosed", err)
	}
}

func TestNameAndTrusted(t *testing.T) {
	t.Parallel()

	wc := New(nil, nil, slog.Default())
	if wc.Name() != "web" {
		t.Errorf("Name() = %q", wc.Name())
	}
	if !wc.Trusted() {
		t.Error("web should be trusted")
	}
}

func TestStartStop(t *testing.T) {
	t.Parallel()

	wc := New(nil, nil, slog.Default())
	if err := wc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := wc.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestChatEventToMessage(t *testing.T) {
	t.Parallel()

	evt := ipc.ChatEvent{
		Type:       "tool_call",
		ToolCallID: "tc-1",
		ToolName:   "Bash",
		Input:      `{"command":"ls"}`,
		Status:     "running",
		IsMeta:     true,
	}

	msg := chatEventToMessage(evt)
	if msg.Type != "tool_call" {
		t.Errorf("type = %q", msg.Type)
	}
	if msg.Role != "assistant" {
		t.Errorf("role = %q", msg.Role)
	}
	if msg.ToolCallID != "tc-1" {
		t.Errorf("tool_call_id = %q", msg.ToolCallID)
	}
	if msg.ToolName != "Bash" {
		t.Errorf("tool_name = %q", msg.ToolName)
	}
	if !msg.IsMeta {
		t.Error("expected IsMeta = true")
	}
}

func TestSupportedMIME(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mediaType string
		want      bool
	}{
		// Images
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", true},

		// Text
		{"text/plain", true},
		{"text/html", true},
		{"text/csv", true},
		{"text/markdown", true},

		// Application types
		{"application/json", true},
		{"application/xml", true},
		{"application/yaml", true},
		{"application/x-yaml", true},
		{"application/pdf", true},

		// Unsupported
		{"application/octet-stream", false},
		{"application/zip", false},
		{"audio/mpeg", false},
		{"video/mp4", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			t.Parallel()
			got := supportedMIME(tt.mediaType)
			if got != tt.want {
				t.Fatalf("supportedMIME(%q) = %v, want %v", tt.mediaType, got, tt.want)
			}
		})
	}
}

func TestProcessAttachments_NilEmpty(t *testing.T) {
	t.Parallel()

	files, err := processAttachments(nil)
	if err != nil {
		t.Fatalf("unexpected error for nil: %v", err)
	}
	if files != nil {
		t.Fatalf("expected nil for nil input, got %v", files)
	}

	files, err = processAttachments([]ChatAttachment{})
	if err != nil {
		t.Fatalf("unexpected error for empty: %v", err)
	}
	if files != nil {
		t.Fatalf("expected nil for empty input, got %v", files)
	}
}

func TestProcessAttachments_Valid(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(data)

	attachments := []ChatAttachment{
		{Filename: "test.txt", Data: encoded, MediaType: "text/plain"},
		{Filename: "img.png", Data: encoded, MediaType: "image/png"},
	}

	files, err := processAttachments(attachments)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].Filename != "test.txt" {
		t.Errorf("filename = %q, want %q", files[0].Filename, "test.txt")
	}
	if files[0].MediaType != "text/plain" {
		t.Errorf("media type = %q, want %q", files[0].MediaType, "text/plain")
	}
	if string(files[0].Data) != "hello world" {
		t.Errorf("data = %q, want %q", string(files[0].Data), "hello world")
	}
}

func TestProcessAttachments_TooMany(t *testing.T) {
	t.Parallel()

	attachments := make([]ChatAttachment, maxAttachments+1)
	for i := range attachments {
		attachments[i] = ChatAttachment{
			Filename:  "file.txt",
			Data:      base64.StdEncoding.EncodeToString([]byte("x")),
			MediaType: "text/plain",
		}
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for too many attachments")
	}
	if !strings.Contains(err.Error(), "too many attachments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_UnsupportedMIME(t *testing.T) {
	t.Parallel()

	attachments := []ChatAttachment{
		{Filename: "archive.zip", Data: base64.StdEncoding.EncodeToString([]byte("x")), MediaType: "application/zip"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for unsupported MIME type")
	}
	if !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_OversizedBase64(t *testing.T) {
	t.Parallel()

	// Create base64 data that exceeds the pre-decode size check.
	bigData := strings.Repeat("A", maxAttachmentSize*4/3+2048)
	attachments := []ChatAttachment{
		{Filename: "big.txt", Data: bigData, MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for oversized base64")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_InvalidBase64(t *testing.T) {
	t.Parallel()

	attachments := []ChatAttachment{
		{Filename: "bad.txt", Data: "not-valid-base64!!!", MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if !strings.Contains(err.Error(), "invalid base64") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_OversizedDecoded(t *testing.T) {
	t.Parallel()

	raw := make([]byte, maxAttachmentSize+1)
	for i := range raw {
		raw[i] = 'A'
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	attachments := []ChatAttachment{
		{Filename: "big.txt", Data: encoded, MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for oversized decoded data")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestHandleConn_EnsureSessionFailure verifies that when EnsureSession fails,
// the connection is closed with an error and no binding is created.
func TestHandleConn_EnsureSessionFailure(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.ensureSessionFn = func(context.Context, string, string) (string, error) {
		return "", errors.New("instance stopped")
	}

	router := channel.NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
	wc := New(router, mgr, slog.Default())
	router.Register(wc)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Accept(w, r, nil)
		if err != nil {
			t.Logf("ws accept error: %v", err)
			return
		}
		defer conn.Close(ws.StatusNormalClosure, "")
		wc.HandleConn(r.Context(), conn, "inst-1", "web:fail-test")
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := ws.Dial(t.Context(), url, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Should receive an error message then the connection should close.
	var msg ChatMessage
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read error message: %v", err)
	}
	if msg.Type != "error" {
		t.Errorf("expected error message type, got %q", msg.Type)
	}
	if !strings.Contains(msg.Content, "instance stopped") {
		t.Errorf("error content = %q, want substring 'instance stopped'", msg.Content)
	}

	// Next read should fail because the server closed the connection.
	var next ChatMessage
	readCtx, readCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer readCancel()
	if err := wsjson.Read(readCtx, conn, &next); err == nil {
		t.Errorf("expected connection closed error, but read succeeded: %+v", next)
	}

	// Verify no binding was created for this connection.
	wc.mu.Lock()
	_, bound := wc.conns["web:fail-test"]
	wc.mu.Unlock()
	if bound {
		t.Error("connection should not be bound after EnsureSession failure")
	}
}
