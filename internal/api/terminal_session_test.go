package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// --- replayBuffer tests ---

func TestReplayBuffer_WriteAndBytes(t *testing.T) {
	t.Parallel()

	rb := newReplayBuffer(100)
	rb.Write([]byte("hello"))
	rb.Write([]byte(" world"))

	got := string(rb.Bytes())
	if got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestReplayBuffer_Empty(t *testing.T) {
	t.Parallel()

	rb := newReplayBuffer(100)
	got := rb.Bytes()
	if len(got) != 0 {
		t.Fatalf("expected empty bytes, got %d bytes", len(got))
	}
}

func TestReplayBuffer_CircularOverflow(t *testing.T) {
	t.Parallel()

	rb := newReplayBuffer(10)
	rb.Write([]byte("abcdefghij")) // exactly capacity
	got := string(rb.Bytes())
	if got != "abcdefghij" {
		t.Fatalf("got %q, want %q", got, "abcdefghij")
	}

	rb.Write([]byte("klm")) // overflow: discard oldest 3
	got = string(rb.Bytes())
	if got != "defghijklm" {
		t.Fatalf("got %q, want %q", got, "defghijklm")
	}
}

func TestReplayBuffer_OverflowMultipleWrites(t *testing.T) {
	t.Parallel()

	rb := newReplayBuffer(5)
	rb.Write([]byte("abc"))
	rb.Write([]byte("defgh")) // total 8, cap 5 -> keep "defgh"
	got := string(rb.Bytes())
	if got != "defgh" {
		t.Fatalf("got %q, want %q", got, "defgh")
	}
}

func TestReplayBuffer_BytesReturnsCopy(t *testing.T) {
	t.Parallel()

	rb := newReplayBuffer(100)
	rb.Write([]byte("original"))
	got := rb.Bytes()
	got[0] = 'X' // mutate the copy
	if string(rb.Bytes()) != "original" {
		t.Fatal("Bytes() should return a copy, not a reference")
	}
}

// --- TerminalSession subscribe/unsubscribe tests ---

func TestTerminalSession_SubscribeUnsubscribe(t *testing.T) {
	t.Parallel()

	sess := &TerminalSession{
		subs: make(map[string]chan sessionEvent),
	}

	id, ch := sess.subscribe()
	if id == "" {
		t.Fatal("subscribe returned empty ID")
	}
	if ch == nil {
		t.Fatal("subscribe returned nil channel")
	}

	if !sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = true after subscribe")
	}

	sess.unsubscribe(id)
	if sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = false after unsubscribe")
	}
}

func TestTerminalSession_HasSubscribers(t *testing.T) {
	t.Parallel()

	sess := &TerminalSession{
		subs: make(map[string]chan sessionEvent),
	}

	if sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = false with no subscribers")
	}

	id1, _ := sess.subscribe()
	id2, _ := sess.subscribe()
	if !sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = true with 2 subscribers")
	}

	sess.unsubscribe(id1)
	if !sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = true with 1 subscriber remaining")
	}

	sess.unsubscribe(id2)
	if sess.hasSubscribers() {
		t.Fatal("expected hasSubscribers() = false with no subscribers")
	}
}

// --- fanOutData / fanOutExit tests ---

func TestTerminalSession_FanOutData(t *testing.T) {
	t.Parallel()

	sess := &TerminalSession{
		subs: make(map[string]chan sessionEvent),
	}

	_, ch1 := sess.subscribe()
	_, ch2 := sess.subscribe()

	sess.fanOutData([]byte("test data"))

	// Both subscribers should receive the data.
	select {
	case evt := <-ch1:
		if string(evt.data) != "test data" {
			t.Fatalf("subscriber 1 got %q, want %q", evt.data, "test data")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 did not receive data")
	}

	select {
	case evt := <-ch2:
		if string(evt.data) != "test data" {
			t.Fatalf("subscriber 2 got %q, want %q", evt.data, "test data")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 did not receive data")
	}
}

func TestTerminalSession_FanOutExit(t *testing.T) {
	t.Parallel()

	sess := &TerminalSession{
		subs: make(map[string]chan sessionEvent),
	}

	_, ch := sess.subscribe()
	sess.fanOutExit(42)

	select {
	case evt := <-ch:
		if !evt.exited {
			t.Fatal("expected exited=true")
		}
		if evt.exitCode != 42 {
			t.Fatalf("exit code = %d, want 42", evt.exitCode)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive exit event")
	}
}

func TestTerminalSession_FanOutData_CopiesInput(t *testing.T) {
	t.Parallel()

	sess := &TerminalSession{
		subs: make(map[string]chan sessionEvent),
	}

	_, ch := sess.subscribe()

	data := []byte("mutable")
	sess.fanOutData(data)
	data[0] = 'X' // mutate original

	select {
	case evt := <-ch:
		if string(evt.data) != "mutable" {
			t.Fatalf("fanOutData should copy data, got %q", evt.data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// --- snapshot tests ---

func TestTerminalSession_Snapshot(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sess := &TerminalSession{
		exited:   true,
		exitCode: 7,
		lastUsed: now,
	}

	exited, exitCode, lastUsed := sess.snapshot()
	if !exited {
		t.Fatal("expected exited=true")
	}
	if exitCode != 7 {
		t.Fatalf("exitCode = %d, want 7", exitCode)
	}
	if !lastUsed.Equal(now) {
		t.Fatalf("lastUsed = %v, want %v", lastUsed, now)
	}
}

// --- marshalControl / marshalOutput / padSessionID tests ---

func TestPadSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
	}{
		{"short ID", "abc"},
		{"exact length", strings.Repeat("a", sessionIDLen)},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			padded := padSessionID(tt.id)
			if len(padded) != sessionIDLen {
				t.Fatalf("len(padSessionID(%q)) = %d, want %d", tt.id, len(padded), sessionIDLen)
			}
			// Verify the ID is at the start.
			trimmed := strings.TrimRight(string(padded), "\x00")
			if trimmed != tt.id {
				t.Fatalf("padded content = %q, want %q", trimmed, tt.id)
			}
		})
	}
}

func TestMarshalOutput(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	frame := marshalOutput("sess123", data)

	// First byte is message type.
	if frame[0] != termMsgOutput {
		t.Fatalf("frame[0] = 0x%02x, want 0x%02x", frame[0], termMsgOutput)
	}

	// Session ID field.
	sessBytes := frame[1 : 1+sessionIDLen]
	sessID := strings.TrimRight(string(sessBytes), "\x00")
	if sessID != "sess123" {
		t.Fatalf("session ID = %q, want %q", sessID, "sess123")
	}

	// Payload.
	payload := frame[1+sessionIDLen:]
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want %q", payload, "hello")
	}
}

func TestMarshalControl(t *testing.T) {
	t.Parallel()

	msg := termControlMsg{Type: "created", SessionID: "s1"}
	frame := marshalControl("sess456", msg)

	if frame[0] != termMsgControl {
		t.Fatalf("frame[0] = 0x%02x, want 0x%02x", frame[0], termMsgControl)
	}

	sessBytes := frame[1 : 1+sessionIDLen]
	sessID := strings.TrimRight(string(sessBytes), "\x00")
	if sessID != "sess456" {
		t.Fatalf("session ID = %q, want %q", sessID, "sess456")
	}

	payload := frame[1+sessionIDLen:]
	var decoded termControlMsg
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal control payload: %v", err)
	}
	if decoded.Type != "created" {
		t.Fatalf("type = %q, want %q", decoded.Type, "created")
	}
}

// --- applyDefaultSize tests ---

func TestApplyDefaultSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cols, rows        uint16
		wantCols, wantRows uint16
	}{
		{"both zero", 0, 0, defaultTermCols, defaultTermRows},
		{"cols zero", 0, 50, defaultTermCols, 50},
		{"rows zero", 120, 0, 120, defaultTermRows},
		{"both set", 120, 50, 120, 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotCols, gotRows := applyDefaultSize(tt.cols, tt.rows)
			if gotCols != tt.wantCols || gotRows != tt.wantRows {
				t.Fatalf("applyDefaultSize(%d, %d) = (%d, %d), want (%d, %d)",
					tt.cols, tt.rows, gotCols, gotRows, tt.wantCols, tt.wantRows)
			}
		})
	}
}

// --- terminalEnvForSession tests ---

func TestTerminalEnvForSession(t *testing.T) {
	t.Parallel()

	env := terminalEnvForSession("/some/root")

	// Check required env vars.
	hasEnv := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	if !hasEnv("TERM=") {
		t.Error("missing TERM env var")
	}
	if !hasEnv("LANG=") {
		t.Error("missing LANG env var")
	}

	// PATH should be included if it exists in the current environment.
	if os.Getenv("PATH") != "" && !hasEnv("PATH=") {
		t.Error("missing PATH env var")
	}
}

// --- generateSessionID tests ---

func TestGenerateSessionID(t *testing.T) {
	t.Parallel()

	id := generateSessionID()
	if len(id) != 32 {
		t.Fatalf("len(generateSessionID()) = %d, want 32", len(id))
	}

	// Verify it's valid hex.
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex character %q in session ID %q", c, id)
		}
	}

	// IDs should be unique.
	id2 := generateSessionID()
	if id == id2 {
		t.Fatal("two generated session IDs should not be equal")
	}
}

// --- checkLimits tests ---

func TestCheckLimits_PerNode(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	// Fill up per-node limit.
	for range maxSessionsPerNode {
		sess, err := mgr.Create("home", 80, 24)
		if err != nil {
			t.Fatalf("unexpected error creating session: %v", err)
		}
		_ = sess
	}

	// Next one should fail.
	_, err := mgr.Create("home", 80, 24)
	if err == nil {
		t.Fatal("expected error when per-node limit exceeded")
	}
	if !strings.Contains(err.Error(), "too many terminal sessions on node") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckLimits_Global(t *testing.T) {
	t.Parallel()

	// We can't easily test the global limit without creating maxTotalSessions
	// sessions. The per-node limit (5) kicks in first for a single node.
	// Just verify the constant relationship.
	if maxTotalSessions < maxSessionsPerNode {
		t.Fatalf("maxTotalSessions (%d) should be >= maxSessionsPerNode (%d)",
			maxTotalSessions, maxSessionsPerNode)
	}
}

// --- TerminalSessionManager integration tests ---

func TestTerminalSessionManager_CreateAndList(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("session ID is empty")
	}
	if sess.NodeID != "home" {
		t.Fatalf("node ID = %q, want %q", sess.NodeID, "home")
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d sessions, want 1", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("listed ID = %q, want %q", list[0].ID, sess.ID)
	}
}

func TestTerminalSessionManager_CreateEmptyNodeID(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("", 0, 0)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.NodeID != nodeIDHome {
		t.Fatalf("node ID = %q, want %q", sess.NodeID, nodeIDHome)
	}
}

func TestTerminalSessionManager_Close(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := mgr.Close(sess.ID); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	list := mgr.List()
	if len(list) != 0 {
		t.Fatalf("List() returned %d sessions after close, want 0", len(list))
	}
}

func TestTerminalSessionManager_CloseNotFound(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	err := mgr.Close("nonexistent")
	if err == nil {
		t.Fatal("expected error closing nonexistent session")
	}
}

func TestTerminalSessionManager_AttachDetach(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	subID, ch, replay, exited, _, err := mgr.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	if subID == "" {
		t.Fatal("subscriber ID is empty")
	}
	if ch == nil {
		t.Fatal("channel is nil")
	}
	// Replay may be empty or contain shell prompt — just check it doesn't error.
	_ = replay
	_ = exited

	mgr.Detach(sess.ID, subID)
}

func TestTerminalSessionManager_AttachNotFound(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	_, _, _, _, _, err := mgr.Attach("nonexistent")
	if err == nil {
		t.Fatal("expected error attaching to nonexistent session")
	}
}

func TestTerminalSessionManager_WriteInput(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Writing input should not error for an active session.
	if err := mgr.WriteInput(sess.ID, []byte("echo hello\n")); err != nil {
		t.Fatalf("WriteInput failed: %v", err)
	}
}

func TestTerminalSessionManager_WriteInputNotFound(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	err := mgr.WriteInput("nonexistent", []byte("hello"))
	if err == nil {
		t.Fatal("expected error writing to nonexistent session")
	}
}

func TestTerminalSessionManager_Resize(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	sess, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := mgr.Resize(sess.ID, 120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestTerminalSessionManager_ResizeNotFound(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())
	t.Cleanup(func() { mgr.Shutdown() })

	err := mgr.Resize("nonexistent", 80, 24)
	if err == nil {
		t.Fatal("expected error resizing nonexistent session")
	}
}

func TestTerminalSessionManager_Shutdown(t *testing.T) {
	t.Parallel()

	mgr := NewTerminalSessionManager(t.TempDir(), slog.Default())

	_, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	_, err = mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	mgr.Shutdown()

	list := mgr.List()
	if len(list) != 0 {
		t.Fatalf("List() returned %d sessions after Shutdown, want 0", len(list))
	}
}
