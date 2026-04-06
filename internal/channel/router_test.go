package channel

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"path/filepath"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// --- Mocks ---

type mockManager struct {
	mu             sync.Mutex
	sendMessageFn  func(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	sendFilesFn    func(ctx context.Context, instanceID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error)
	sendMetaFn     func(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error)
	newSessionFn   func(instanceID string) (string, error)
	updateConfigFn func(ctx context.Context, instanceID, model string, re *string) error
	notifications  map[string]*inference.NotificationQueue
	activeSessions map[string]string
	instances      map[string]agent.InstanceInfo
	agentInstances map[string]string // agentName → instanceID
}

func newMockManager() *mockManager {
	return &mockManager{
		notifications:  make(map[string]*inference.NotificationQueue),
		activeSessions: make(map[string]string),
		instances:      make(map[string]agent.InstanceInfo),
		agentInstances: make(map[string]string),
	}
}

func (m *mockManager) SendMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, instanceID, message, onEvent)
	}
	return "response", nil
}

func (m *mockManager) SendMessageWithFiles(ctx context.Context, instanceID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	if m.sendFilesFn != nil {
		return m.sendFilesFn(ctx, instanceID, message, files, onEvent)
	}
	return "response", nil
}

func (m *mockManager) SendMetaMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if m.sendMetaFn != nil {
		return m.sendMetaFn(ctx, instanceID, message, onEvent)
	}
	return "meta-response", nil
}

func (m *mockManager) NewSession(instanceID string) (string, error) {
	if m.newSessionFn != nil {
		return m.newSessionFn(instanceID)
	}
	return "new-session-id", nil
}

func (m *mockManager) UpdateInstanceConfig(ctx context.Context, instanceID, model string, re *string) error {
	if m.updateConfigFn != nil {
		return m.updateConfigFn(ctx, instanceID, model, re)
	}
	return nil
}

func (m *mockManager) InstanceNotifications(instanceID string) *inference.NotificationQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notifications[instanceID]
}

func (m *mockManager) ActiveSessionID(instanceID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeSessions[instanceID]
}

func (m *mockManager) GetInstance(instanceID string) (agent.InstanceInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.instances[instanceID]
	return info, ok
}

func (m *mockManager) InstanceByAgentName(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.agentInstances[name]
	return id, ok
}

type mockCmdHandler struct {
	fn func(input string) (string, error)
}

func (h *mockCmdHandler) HandleCommand(input string) (string, error) {
	if h.fn != nil {
		return h.fn(input)
	}
	return "ok", nil
}

type mockChannel struct {
	name      string
	trusted   bool
	deliverFn func(ctx context.Context, key string, events []ipc.ChatEvent, result TurnResult) error
}

func (c *mockChannel) Name() string                  { return c.name }
func (c *mockChannel) Trusted() bool                 { return c.trusted }
func (c *mockChannel) Start(_ context.Context) error { return nil }
func (c *mockChannel) Stop() error                   { return nil }
func (c *mockChannel) Deliver(ctx context.Context, key string, events []ipc.ChatEvent, result TurnResult) error {
	if c.deliverFn != nil {
		return c.deliverFn(ctx, key, events, result)
	}
	return nil
}

func testRouter(t *testing.T, mgr *mockManager) *Router {
	t.Helper()
	return NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
}

// --- Tests ---

func TestDispatch_SendMessage(t *testing.T) {
	t.Parallel()

	var capturedMsg string
	mgr := newMockManager()
	mgr.sendMessageFn = func(_ context.Context, _ string, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
		capturedMsg = message
		_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "hello"})
		return "hello", nil
	}

	r := testRouter(t, mgr)

	var gotEvents []ipc.ChatEvent
	var gotResult TurnResult
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "hi there",
		OnEvent: func(evt ipc.ChatEvent) error {
			gotEvents = append(gotEvents, evt)
			return nil
		},
		OnDone: func(result TurnResult) error {
			gotResult = result
			return nil
		},
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if capturedMsg != "hi there" {
		t.Errorf("message = %q, want %q", capturedMsg, "hi there")
	}
	if len(gotEvents) != 1 || gotEvents[0].Content != "hello" {
		t.Errorf("events = %v, want 1 delta event", gotEvents)
	}
	if gotResult.Response != "hello" {
		t.Errorf("response = %q, want %q", gotResult.Response, "hello")
	}
}

func TestDispatch_SendMessageWithFiles(t *testing.T) {
	t.Parallel()

	var capturedFiles []fantasy.FilePart
	mgr := newMockManager()
	mgr.sendFilesFn = func(_ context.Context, _ string, _ string, files []fantasy.FilePart, _ func(ipc.ChatEvent) error) (string, error) {
		capturedFiles = files
		return "got files", nil
	}

	r := testRouter(t, mgr)

	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "check this",
		Files:           []fantasy.FilePart{{Filename: "test.txt", Data: []byte("hi")}},
		OnEvent:         func(ipc.ChatEvent) error { return nil },
		OnDone:          func(TurnResult) error { return nil },
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(capturedFiles) != 1 || capturedFiles[0].Filename != "test.txt" {
		t.Errorf("files = %v, want 1 file", capturedFiles)
	}
}

func TestDispatch_ErrorReportedToChannel(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.sendMessageFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		return "", errors.New("inference failed")
	}

	r := testRouter(t, mgr)

	var gotError bool
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "hi",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "error" {
				gotError = true
			}
			return nil
		},
		OnDone: func(TurnResult) error { return nil },
	}

	_ = r.Dispatch(context.Background(), msg)
	if !gotError {
		t.Error("expected error event to be sent to channel")
	}
}

func TestSlashCommand_Clear(t *testing.T) {
	t.Parallel()

	var clearCalled atomic.Bool
	mgr := newMockManager()
	mgr.newSessionFn = func(string) (string, error) {
		clearCalled.Store(true)
		return "new-session", nil
	}

	r := testRouter(t, mgr)

	var gotClear, gotDone bool
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "/clear",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "clear" {
				gotClear = true
			}
			return nil
		},
		OnDone: func(TurnResult) error {
			gotDone = true
			return nil
		},
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if !gotClear {
		t.Error("expected clear event")
	}
	if !gotDone {
		t.Error("expected done callback")
	}

	// NewSession runs async — wait briefly.
	time.Sleep(50 * time.Millisecond)
	if !clearCalled.Load() {
		t.Error("expected NewSession to be called")
	}
}

func TestSlashCommand_Help(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	handler := &mockCmdHandler{fn: func(input string) (string, error) {
		if strings.HasPrefix(input, "/help") {
			return "available commands: ...", nil
		}
		return "", errors.New("unknown")
	}}

	r := NewRouter(t.Context(), mgr, handler, nil, slog.Default())

	var gotSystem string
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "/help",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "system" {
				gotSystem = evt.Content
			}
			return nil
		},
		OnDone: func(TurnResult) error { return nil },
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if gotSystem != "available commands: ..." {
		t.Errorf("system content = %q, want help text", gotSystem)
	}
}

func TestSlashCommand_TrustGating(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	handler := &mockCmdHandler{}

	r := NewRouter(t.Context(), mgr, handler, nil, slog.Default())

	// Register an untrusted channel.
	untrusted := &mockChannel{name: "telegram", trusted: false}
	r.Register(untrusted)

	for _, cmd := range []string{"/secrets list", "/tools list", "/cluster"} {
		var gotSystem string
		msg := InboundMessage{
			ConversationKey: "test:1",
			InstanceID:      "inst-1",
			ChannelName:     "telegram",
			Text:            cmd,
			OnEvent: func(evt ipc.ChatEvent) error {
				if evt.Type == "system" {
					gotSystem = evt.Content
				}
				return nil
			},
			OnDone: func(TurnResult) error { return nil },
		}

		if err := r.Dispatch(context.Background(), msg); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(gotSystem, "only available from the web interface") {
			t.Errorf("cmd %q: expected trust rejection, got %q", cmd, gotSystem)
		}
	}

	// Trusted channel should pass through.
	trusted := &mockChannel{name: "web", trusted: true}
	r.Register(trusted)

	var cmdHandlerCalled bool
	handler.fn = func(string) (string, error) {
		cmdHandlerCalled = true
		return "ok", nil
	}

	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "web",
		Text:            "/secrets list",
		OnEvent:         func(ipc.ChatEvent) error { return nil },
		OnDone:          func(TurnResult) error { return nil },
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if !cmdHandlerCalled {
		t.Error("expected command handler to be called for trusted channel")
	}
}

func TestSlashCommand_EmptySlash(t *testing.T) {
	t.Parallel()

	r := testRouter(t, newMockManager())

	var gotSystem string
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "/",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "system" {
				gotSystem = evt.Content
			}
			return nil
		},
		OnDone: func(TurnResult) error { return nil },
	}

	_ = r.Dispatch(context.Background(), msg)
	if !strings.Contains(gotSystem, "/help") {
		t.Errorf("expected help hint, got %q", gotSystem)
	}
}

func TestSlashCommand_UnknownCommand(t *testing.T) {
	t.Parallel()

	r := NewRouter(t.Context(), newMockManager(), nil, nil, slog.Default())

	var gotSystem string
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "/foobar",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "system" {
				gotSystem = evt.Content
			}
			return nil
		},
		OnDone: func(TurnResult) error { return nil },
	}

	_ = r.Dispatch(context.Background(), msg)
	if !strings.Contains(gotSystem, "Unknown command") {
		t.Errorf("expected unknown command error, got %q", gotSystem)
	}
}

func TestSlashCommand_HandlerError(t *testing.T) {
	t.Parallel()

	handler := &mockCmdHandler{fn: func(string) (string, error) {
		return "", errors.New("bad command")
	}}
	r := NewRouter(t.Context(), newMockManager(), handler, nil, slog.Default())

	var gotSystem string
	msg := InboundMessage{
		ConversationKey: "test:1",
		InstanceID:      "inst-1",
		ChannelName:     "test",
		Text:            "/bad",
		OnEvent: func(evt ipc.ChatEvent) error {
			if evt.Type == "system" {
				gotSystem = evt.Content
			}
			return nil
		},
		OnDone: func(TurnResult) error { return nil },
	}

	_ = r.Dispatch(context.Background(), msg)
	if !strings.Contains(gotSystem, "bad command") {
		t.Errorf("expected error message, got %q", gotSystem)
	}
}

func TestBindUnbind(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}

	r := testRouter(t, mgr)
	ch := &mockChannel{name: "test"}
	r.Register(ch)

	r.Bind("key-1", "test", "inst-1")

	bindings := r.bindingsForInstance("inst-1")
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(bindings))
	}
	if bindings[0].ConversationKey != "key-1" {
		t.Errorf("key = %q, want %q", bindings[0].ConversationKey, "key-1")
	}

	r.Unbind("key-1")
	bindings = r.bindingsForInstance("inst-1")
	if len(bindings) != 0 {
		t.Errorf("got %d bindings after unbind, want 0", len(bindings))
	}
}

func TestBindingResolveInstanceID(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.agentInstances["operator"] = "inst-1"

	// Resolve by instance ID.
	b := &Binding{Target: "inst-1"}
	if id := b.ResolveInstanceID(mgr); id != "inst-1" {
		t.Errorf("resolve by ID = %q, want %q", id, "inst-1")
	}

	// Resolve by agent name.
	b2 := &Binding{Target: "operator"}
	if id := b2.ResolveInstanceID(mgr); id != "inst-1" {
		t.Errorf("resolve by name = %q, want %q", id, "inst-1")
	}

	// Unknown target.
	b3 := &Binding{Target: "unknown"}
	if id := b3.ResolveInstanceID(mgr); id != "" {
		t.Errorf("resolve unknown = %q, want empty", id)
	}

	// Agent name exists but instance is stopped (running=false).
	stoppedMgr := newMockManager()
	stoppedMgr.agentInstances["stopped-agent"] = "" // empty ID = not running
	b4 := &Binding{Target: "stopped-agent"}
	if id := b4.ResolveInstanceID(stoppedMgr); id != "" {
		t.Errorf("resolve stopped agent = %q, want empty", id)
	}
}

func TestNotificationPump_DeliverToBindings(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.activeSessions["inst-1"] = "session-1"
	q := inference.NewNotificationQueue(slog.Default())
	mgr.notifications["inst-1"] = q

	mgr.sendMetaFn = func(_ context.Context, _ string, msg string, onEvent func(ipc.ChatEvent) error) (string, error) {
		_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "notification: " + msg})
		return "meta-response", nil
	}

	r := testRouter(t, mgr)

	var deliveredEvents []ipc.ChatEvent
	var deliveredMu sync.Mutex
	ch := &mockChannel{
		name:    "test",
		trusted: true,
		deliverFn: func(_ context.Context, _ string, events []ipc.ChatEvent, _ TurnResult) error {
			deliveredMu.Lock()
			deliveredEvents = append(deliveredEvents, events...)
			deliveredMu.Unlock()
			return nil
		},
	}
	r.Register(ch)
	r.Bind("key-1", "test", "inst-1")

	r.StartNotificationPump(t.Context(), "inst-1")

	// Push a notification.
	q.Push(inference.Notification{Content: "hello", Source: "test"})

	// Wait for delivery.
	deadline := time.After(2 * time.Second)
	for {
		deliveredMu.Lock()
		n := len(deliveredEvents)
		deliveredMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for notification delivery")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	deliveredMu.Lock()
	defer deliveredMu.Unlock()
	if len(deliveredEvents) != 1 || deliveredEvents[0].Content != "notification: hello" {
		t.Errorf("events = %v, want notification event", deliveredEvents)
	}
}

func TestNotificationPump_StaleSessionDiscarded(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	mgr.activeSessions["inst-1"] = "session-2"
	q := inference.NewNotificationQueue(slog.Default())
	mgr.notifications["inst-1"] = q

	var metaCalled atomic.Bool
	mgr.sendMetaFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		metaCalled.Store(true)
		return "", nil
	}

	r := testRouter(t, mgr)
	ch := &mockChannel{name: "test"}
	r.Register(ch)
	r.Bind("key-1", "test", "inst-1")

	r.StartNotificationPump(t.Context(), "inst-1")

	// Push a notification scoped to a different session — should be discarded.
	q.Push(inference.Notification{Content: "stale", Source: "test", SessionID: "session-1"})

	time.Sleep(100 * time.Millisecond)
	if metaCalled.Load() {
		t.Error("SendMetaMessage should not be called for stale session notifications")
	}
}

func TestNotificationPump_NoBindingsSkipsDelivery(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	q := inference.NewNotificationQueue(slog.Default())
	mgr.notifications["inst-1"] = q

	var metaCalled atomic.Bool
	mgr.sendMetaFn = func(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
		metaCalled.Store(true)
		return "", nil
	}

	r := testRouter(t, mgr)
	// No bindings registered.

	r.StartNotificationPump(t.Context(), "inst-1")

	q.Push(inference.Notification{Content: "orphan", Source: "test"})
	time.Sleep(100 * time.Millisecond)

	if metaCalled.Load() {
		t.Error("should not run meta turn when no bindings exist")
	}
}

func TestNotificationPump_Idempotent(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	q := inference.NewNotificationQueue(slog.Default())
	mgr.notifications["inst-1"] = q

	r := testRouter(t, mgr)

	// Starting the pump twice should not panic or create duplicate goroutines.
	r.StartNotificationPump(t.Context(), "inst-1")
	r.StartNotificationPump(t.Context(), "inst-1")

	r.StopNotificationPump("inst-1")
}

func TestFanOut_ChannelClosedUnbinds(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}

	r := testRouter(t, mgr)

	ch := &mockChannel{
		name: "test",
		deliverFn: func(context.Context, string, []ipc.ChatEvent, TurnResult) error {
			return ErrChannelClosed
		},
	}
	r.Register(ch)
	r.Bind("key-1", "test", "inst-1")

	// Fan out should unbind the key.
	bindings := r.bindingsForInstance("inst-1")
	r.fanOut(context.Background(), bindings, nil, TurnResult{})

	remaining := r.bindingsForInstance("inst-1")
	if len(remaining) != 0 {
		t.Errorf("expected binding to be removed after ErrChannelClosed, got %d", len(remaining))
	}
}

func TestFanOut_ConcurrentDelivery(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}

	r := testRouter(t, mgr)

	var deliveryCount atomic.Int32
	for i := range 3 {
		ch := &mockChannel{
			name: "test",
			deliverFn: func(context.Context, string, []ipc.ChatEvent, TurnResult) error {
				time.Sleep(10 * time.Millisecond)
				deliveryCount.Add(1)
				return nil
			},
		}
		r.Register(ch)
		r.Bind("key-"+string(rune('a'+i)), "test", "inst-1")
	}

	bindings := r.bindingsForInstance("inst-1")
	start := time.Now()
	r.fanOut(context.Background(), bindings, nil, TurnResult{})
	elapsed := time.Since(start)

	if deliveryCount.Load() != 3 {
		t.Errorf("delivery count = %d, want 3", deliveryCount.Load())
	}
	// Concurrent delivery — should take ~10ms, not ~30ms.
	if elapsed > 100*time.Millisecond {
		t.Errorf("fan-out took %v — not running concurrently", elapsed)
	}
}

func TestRouterStop(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	q := inference.NewNotificationQueue(slog.Default())
	mgr.notifications["inst-1"] = q

	var stopped atomic.Bool
	ch := &mockChannel{
		name: "test",
		deliverFn: func(context.Context, string, []ipc.ChatEvent, TurnResult) error {
			return nil
		},
	}
	// Override Stop to track it.
	stoppableCh := &stoppableChannel{mockChannel: ch, onStop: func() { stopped.Store(true) }}

	r := NewRouter(t.Context(), mgr, &mockCmdHandler{}, nil, slog.Default())
	r.Register(stoppableCh)

	ctx, cancel := context.WithCancel(context.Background()) //nolint:testingcontext // cancel is used explicitly before Stop
	r.StartNotificationPump(ctx, "inst-1")
	cancel()

	r.Stop()
	if !stopped.Load() {
		t.Error("expected channel Stop to be called")
	}
}

type stoppableChannel struct {
	*mockChannel
	onStop func()
}

func (c *stoppableChannel) Stop() error {
	if c.onStop != nil {
		c.onStop()
	}
	return nil
}

func TestUsageQuerier_NilPDB(t *testing.T) {
	t.Parallel()

	q := &UsageQuerier{PDB: nil}
	result := q.BuildUsageInfo(context.Background(), "inst-1")
	if result != nil {
		t.Errorf("expected nil for nil PDB, got %+v", result)
	}
}

func TestUsageQuerier_Nil(t *testing.T) {
	t.Parallel()

	var q *UsageQuerier
	result := q.BuildUsageInfo(context.Background(), "inst-1")
	if result != nil {
		t.Errorf("expected nil for nil querier, got %+v", result)
	}
}

func TestUsageQuerier_WithDB(t *testing.T) {
	t.Parallel()

	pdb, err := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1", Model: "test-model"}
	mgr.activeSessions["inst-1"] = "session-1"

	q := &UsageQuerier{PDB: pdb, Manager: mgr}
	result := q.BuildUsageInfo(context.Background(), "inst-1")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// No usage data in DB yet, but the model and context window should be set.
	if result.Model != "test-model" {
		t.Errorf("model = %q, want %q", result.Model, "test-model")
	}
	// All token counts should be zero.
	if result.SessionInputTokens != 0 || result.SessionOutputTokens != 0 {
		t.Errorf("expected zero session tokens, got in=%d out=%d",
			result.SessionInputTokens, result.SessionOutputTokens)
	}
}

func TestUsageQuerier_NoManager(t *testing.T) {
	t.Parallel()

	pdb, err := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()

	// No manager — should still return a result with zero values.
	q := &UsageQuerier{PDB: pdb, Manager: nil}
	result := q.BuildUsageInfo(context.Background(), "inst-1")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Model != "" {
		t.Errorf("model = %q, want empty", result.Model)
	}
}

func TestRouter_ChannelAccessor(t *testing.T) {
	t.Parallel()

	r := testRouter(t, newMockManager())

	// No channel registered.
	if ch := r.Channel("web"); ch != nil {
		t.Error("expected nil for unregistered channel")
	}

	// Register and retrieve.
	ch := &mockChannel{name: "web"}
	r.Register(ch)
	if got := r.Channel("web"); got == nil {
		t.Error("expected non-nil")
	} else if got.Name() != "web" {
		t.Errorf("name = %q, want %q", got.Name(), "web")
	}
}

func TestRouter_GetBinding(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	mgr.instances["inst-1"] = agent.InstanceInfo{ID: "inst-1"}
	r := testRouter(t, mgr)

	ch := &mockChannel{name: "test"}
	r.Register(ch)

	// No binding.
	if b := r.GetBinding("key-1"); b != nil {
		t.Error("expected nil for nonexistent binding")
	}

	// Create binding and retrieve.
	r.Bind("key-1", "test", "inst-1")
	b := r.GetBinding("key-1")
	if b == nil {
		t.Fatal("expected non-nil binding")
	}
	if b.Target != "inst-1" {
		t.Errorf("target = %q, want %q", b.Target, "inst-1")
	}
}

func TestRouter_Manager(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	r := testRouter(t, mgr)
	if r.Manager() == nil {
		t.Error("expected non-nil manager")
	}
}

func TestBind_ReturnsBinding(t *testing.T) {
	t.Parallel()

	mgr := newMockManager()
	r := testRouter(t, mgr)
	ch := &mockChannel{name: "test"}
	r.Register(ch)

	b := r.Bind("key-1", "test", "inst-1")
	if b == nil {
		t.Fatal("Bind should return the binding")
	}
	if b.ConversationKey != "key-1" {
		t.Errorf("key = %q", b.ConversationKey)
	}
	if b.Target != "inst-1" {
		t.Errorf("target = %q", b.Target)
	}
}
