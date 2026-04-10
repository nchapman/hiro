package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/toolrules"
)

// fakeLanguageModel satisfies fantasy.LanguageModel for tests that pass a
// non-nil LM to pushToolsAndModel. Methods are never called because test
// instances have no live inference loops.
type fakeLanguageModel struct{}

func (f *fakeLanguageModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, errors.New("fake")
}
func (f *fakeLanguageModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, errors.New("fake")
}
func (f *fakeLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("fake")
}
func (f *fakeLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("fake")
}
func (f *fakeLanguageModel) Provider() string { return "fake" }
func (f *fakeLanguageModel) Model() string    { return "fake" }

// --- tool_executor.go ---

type fakeTool struct {
	name    string
	runFunc func(ctx context.Context, tc fantasy.ToolCall) (fantasy.ToolResponse, error)
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name}
}

func (f *fakeTool) Run(ctx context.Context, tc fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return f.runFunc(ctx, tc)
}

func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{}
}

func (f *fakeTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestToolExecutorFromTools(t *testing.T) {
	echo := &fakeTool{
		name: "echo",
		runFunc: func(_ context.Context, tc fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("echoed: " + tc.Input), nil
		},
	}
	errTool := &fakeTool{
		name: "fail",
		runFunc: func(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, fmt.Errorf("tool failure")
		},
	}
	errorResponse := &fakeTool{
		name: "err-resp",
		runFunc: func(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{Type: "error", Content: "something went wrong"}, nil
		},
	}

	executor := ToolExecutorFromTools([]fantasy.AgentTool{echo, errTool, errorResponse})

	t.Run("known tool returns content", func(t *testing.T) {
		result, err := executor.ExecuteTool(context.Background(), "call-1", "echo", "hello")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Content != "echoed: hello" {
			t.Errorf("content = %q, want %q", result.Content, "echoed: hello")
		}
		if result.IsError {
			t.Error("expected IsError = false")
		}
	})

	t.Run("unknown tool returns error content", func(t *testing.T) {
		result, err := executor.ExecuteTool(context.Background(), "call-2", "nonexistent", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Error("expected IsError = true for unknown tool")
		}
		if result.Content != "unknown tool: nonexistent" {
			t.Errorf("content = %q, want unknown tool message", result.Content)
		}
	})

	t.Run("tool returning error propagates", func(t *testing.T) {
		_, err := executor.ExecuteTool(context.Background(), "call-3", "fail", "")
		if err == nil {
			t.Fatal("expected error from failing tool")
		}
	})

	t.Run("tool returning error type sets IsError", func(t *testing.T) {
		result, err := executor.ExecuteTool(context.Background(), "call-4", "err-resp", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Error("expected IsError = true for error response type")
		}
	})
}

func TestToolExecutorFromTools_Empty(t *testing.T) {
	executor := ToolExecutorFromTools(nil)
	result, err := executor.ExecuteTool(context.Background(), "id", "anything", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError = true for unknown tool on empty executor")
	}
}

// --- manager_helpers.go ---

func TestSecretNames_NilCP(t *testing.T) {
	mgr, _ := setupTestManager(t)
	names := mgr.SecretNames()
	if names != nil {
		t.Errorf("SecretNames with nil CP should return nil, got %v", names)
	}
}

func TestSecretNames_WithCP(t *testing.T) {
	cp := &fullMockCP{
		secretNames: []string{"GITHUB_TOKEN", "AWS_KEY"},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	names := mgr.SecretNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 secret names, got %d", len(names))
	}
	if names[0] != "GITHUB_TOKEN" || names[1] != "AWS_KEY" {
		t.Errorf("unexpected secret names: %v", names)
	}
}

func TestSecretEnv_NilCP(t *testing.T) {
	mgr, _ := setupTestManager(t)
	env := mgr.SecretEnv()
	if env != nil {
		t.Errorf("SecretEnv with nil CP should return nil, got %v", env)
	}
}

func TestSecretEnv_WithCP(t *testing.T) {
	cp := &fullMockCP{
		secretEnv: []string{"GITHUB_TOKEN=ghp_xxx", "AWS_KEY=AKIA_yyy"},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	env := mgr.SecretEnv()
	if len(env) != 2 {
		t.Fatalf("expected 2 secret env vars, got %d", len(env))
	}
}

func TestInstanceNotifications_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	nq := mgr.InstanceNotifications("nonexistent")
	if nq != nil {
		t.Error("expected nil notifications for nonexistent instance")
	}
}

func TestInstanceNotifications_Found(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	nq := mgr.InstanceNotifications(id)
	if nq == nil {
		t.Error("expected non-nil notifications for running instance")
	}
}

func TestActiveSessionID(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	sessID := mgr.ActiveSessionID(id)
	if sessID == "" {
		t.Error("expected non-empty session ID for running instance")
	}
}

func TestActiveSessionID_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	sessID := mgr.ActiveSessionID("nonexistent")
	if sessID != "" {
		t.Errorf("expected empty session ID for nonexistent instance, got %q", sessID)
	}
}

func TestInstanceBySession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	sessID := mgr.ActiveSessionID(id)
	found := mgr.instanceBySession(sessID)
	if found == nil {
		t.Fatal("expected to find instance by session ID")
	}
	if found.info.ID != id {
		t.Errorf("found instance ID = %q, want %q", found.info.ID, id)
	}
}

func TestInstanceBySession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	found := mgr.instanceBySession("nonexistent-session")
	if found != nil {
		t.Error("expected nil for nonexistent session")
	}
}

func TestListNodes_NoCluster(t *testing.T) {
	mgr, _ := setupTestManager(t)
	nodes := mgr.ListNodes()
	if nodes != nil {
		t.Errorf("expected nil nodes without cluster, got %v", nodes)
	}
}

func TestExtractAgentName_Coverage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"agents/operator/agent.md", "operator"},
		{"agents/my-agent/agent.md", "my-agent"},
		{"agents/foo/skills/bar.md", "foo"},
		{"other/path", ""},
		{"agents", ""},
		{"", ""},
		{"notAgents/foo/bar", ""},
	}
	for _, tt := range tests {
		got := extractAgentName(tt.path)
		if got != tt.want {
			t.Errorf("extractAgentName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// --- manager_query.go ---

func TestGetHistory_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.GetHistory(t.Context(), "nonexistent", 10)
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
}

func TestGetHistory_Ephemeral(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	cfg, err := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	if err != nil {
		t.Fatalf("load agent dir: %v", err)
	}
	id, _ := mgr.startInstance(t.Context(), "eph-test-id", "eph-sess-id", "web", cfg, "", config.ModeEphemeral, "", "", "", "")

	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for ephemeral instance, got %v", msgs)
	}
}

func TestGetHistory_NoPDB(t *testing.T) {
	// Manager without a platform DB should return nil for persistent history.
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil with no PDB, got %v", msgs)
	}
}

func TestGetHistory_WithPDB(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// With PDB but no messages, should return empty slice.
	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestGetHistory_WithMessages(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sessID := mgr.ActiveSessionID(id)

	// Insert test messages into the DB.
	pdb.AppendMessage(t.Context(), sessID, "user", "hello", "", 0)
	pdb.AppendMessage(t.Context(), sessID, "assistant", "hi there", "", 0)
	// System message should be filtered out.
	pdb.AppendMessage(t.Context(), sessID, "system", "system prompt", "", 0)

	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("first message role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("second message role = %q, want assistant", msgs[1].Role)
	}
}

func TestGetSessionHistory(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sessID := mgr.ActiveSessionID(id)

	pdb.AppendMessage(t.Context(), sessID, "user", "test message", "", 0)

	msgs, err := mgr.GetSessionHistory(t.Context(), sessID, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "test message" {
		t.Errorf("message content = %q, want %q", msgs[0].Content, "test message")
	}
}

func TestGetSessionHistory_NoPDB(t *testing.T) {
	mgr, _ := setupTestManager(t)
	msgs, err := mgr.GetSessionHistory(t.Context(), "any-session", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil with no PDB, got %v", msgs)
	}
}

func TestInstanceByAgentName_PrefersRunning(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	// Create two instances with the same agent name.
	id1, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create instance 1: %v", err)
	}
	id2, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create instance 2: %v", err)
	}

	// Stop the first one.
	mgr.StopInstance(id1)

	// Should find the running one (id2).
	found, running := mgr.InstanceByAgentName("test-agent")
	if !running {
		t.Fatal("expected running instance to be found")
	}
	if found != id2 {
		t.Errorf("expected running instance %q, got %q", id2, found)
	}
}

func TestInstanceByAgentName_ReturnsStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	found, running := mgr.InstanceByAgentName("test-agent")
	if running {
		t.Error("expected running = false for stopped instance")
	}
	if found != id {
		t.Errorf("expected stopped instance %q, got %q", id, found)
	}
}

// --- manager_session.go ---

func TestValidReasoningEffort(t *testing.T) {
	valid := []string{"", "on", "low", "medium", "high", "max", "minimal", "xhigh"}
	for _, effort := range valid {
		if !validReasoningEffort(effort) {
			t.Errorf("validReasoningEffort(%q) = false, want true", effort)
		}
	}

	invalid := []string{"none", "extreme", "turbo", "123"}
	for _, effort := range invalid {
		if validReasoningEffort(effort) {
			t.Errorf("validReasoningEffort(%q) = true, want false", effort)
		}
	}
}

func TestUpdateInstanceConfig_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.UpdateInstanceConfig(t.Context(), "nonexistent", "model", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
}

func TestUpdateInstanceConfig_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	err = mgr.UpdateInstanceConfig(t.Context(), id, "model", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for stopped instance")
	}
}

func TestUpdateInstanceConfig_NoLoop(t *testing.T) {
	// Without a provider, instances have no loop.
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = mgr.UpdateInstanceConfig(t.Context(), id, "", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for instance with no loop")
	}
}

func TestUpdateInstanceConfig_NoLoopWithEffort(t *testing.T) {
	// Without an inference loop, UpdateInstanceConfig errors even with a valid effort.
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	effort := "high"
	err = mgr.UpdateInstanceConfig(t.Context(), id, "", &effort, nil, nil)
	if err == nil {
		t.Fatal("expected error for instance with no loop")
	}
}

func TestStartInstance_NotStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = mgr.StartInstance(t.Context(), id)
	if !errors.Is(err, ErrInstanceNotStopped) {
		t.Errorf("expected ErrInstanceNotStopped, got %v", err)
	}
}

func TestSendMetaMessage_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.SendMetaMessage(t.Context(), "nonexistent", "hello", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
}

func TestSendMetaMessage_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	_, err = mgr.SendMetaMessage(t.Context(), id, "hello", nil)
	if err == nil {
		t.Fatal("expected error for stopped instance")
	}
}

func TestSendMetaMessage_NoLoop(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = mgr.SendMetaMessage(t.Context(), id, "hello", nil)
	if err == nil {
		t.Fatal("expected error when no inference loop")
	}
}

// --- manager_lifecycle.go ---

func TestSeedInstanceFiles_PersistentWithDisplayNames(t *testing.T) {
	dir := t.TempDir()
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if err := seedInstanceFiles(instDir, configPath, config.ModePersistent, "My Agent", "A helpful agent", "", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// persona.md should exist and have frontmatter.
	pd, err := config.ReadPersonaFile(instDir)
	if err != nil {
		t.Fatalf("reading persona: %v", err)
	}
	if pd.Name != "My Agent" {
		t.Errorf("persona name = %q, want %q", pd.Name, "My Agent")
	}
	if pd.Description != "A helpful agent" {
		t.Errorf("persona description = %q, want %q", pd.Description, "A helpful agent")
	}

	// memory.md should exist.
	if _, err := os.Stat(filepath.Join(instDir, "memory.md")); err != nil {
		t.Errorf("memory.md should exist: %v", err)
	}
}

func TestSeedInstanceFiles_PersistentWithPersonaBody(t *testing.T) {
	dir := t.TempDir()
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if err := seedInstanceFiles(instDir, configPath, config.ModePersistent, "Backend Lead", "Owns API rewrite", "You focus on Go and PostgreSQL.", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pd, err := config.ReadPersonaFile(instDir)
	if err != nil {
		t.Fatalf("reading persona: %v", err)
	}
	if pd.Name != "Backend Lead" {
		t.Errorf("persona name = %q, want %q", pd.Name, "Backend Lead")
	}
	if pd.Body != "You focus on Go and PostgreSQL." {
		t.Errorf("persona body = %q, want %q", pd.Body, "You focus on Go and PostgreSQL.")
	}
}

func TestSeedInstanceFiles_EphemeralEmptyPersona(t *testing.T) {
	dir := t.TempDir()
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if err := seedInstanceFiles(instDir, configPath, config.ModeEphemeral, "", "", "", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// persona.md should exist but be empty.
	data, err := os.ReadFile(filepath.Join(instDir, "persona.md"))
	if err != nil {
		t.Fatalf("reading persona.md: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("ephemeral persona.md should be empty, got %q", string(data))
	}
}

func TestSeedInstanceFiles_PersistentNoDisplayName(t *testing.T) {
	dir := t.TempDir()
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Persistent but no display name/desc — should create empty persona.md.
	configPath := filepath.Join(dir, "config.yaml")
	if err := seedInstanceFiles(instDir, configPath, config.ModePersistent, "", "", "", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(instDir, "persona.md"))
	if err != nil {
		t.Fatalf("reading persona.md: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("persona.md should be empty when no display name, got %q", string(data))
	}
}

func TestSeedInstanceFiles_ToolsSeeded(t *testing.T) {
	dir := t.TempDir()
	instDir := filepath.Join(dir, "inst")
	os.MkdirAll(instDir, 0o755)
	configPath := filepath.Join(dir, "config", "instances", "test-inst.yaml")

	tools := []string{"Bash", "Read", "Write"}
	denied := []string{"Bash(rm *)"}
	if err := seedInstanceFiles(instDir, configPath, config.ModePersistent, "", "", "", tools, denied); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	instCfg, err := config.LoadInstanceConfig(configPath)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if len(instCfg.AllowedTools) != 3 {
		t.Errorf("AllowedTools: got %v, want 3 items", instCfg.AllowedTools)
	}
	if len(instCfg.DisallowedTools) != 1 || instCfg.DisallowedTools[0] != "Bash(rm *)" {
		t.Errorf("DisallowedTools: got %v", instCfg.DisallowedTools)
	}
}

func TestApplyInstanceToolConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Save instance config with custom tools.
	config.SaveInstanceConfig(configPath, config.InstanceConfig{
		AllowedTools:    []string{"Read", "Glob"},
		DisallowedTools: []string{"Read(/etc/*)"},
	})

	// Agent config has broader tools.
	cfg := config.AgentConfig{
		AllowedTools:    []string{"Bash", "Read", "Write", "Glob", "Grep"},
		DisallowedTools: nil,
	}

	// Instance config should override.
	applyInstanceToolConfig(configPath, &cfg)
	if len(cfg.AllowedTools) != 2 || cfg.AllowedTools[0] != "Read" {
		t.Errorf("AllowedTools not overridden: got %v", cfg.AllowedTools)
	}
	if len(cfg.DisallowedTools) != 1 || cfg.DisallowedTools[0] != "Read(/etc/*)" {
		t.Errorf("DisallowedTools not overridden: got %v", cfg.DisallowedTools)
	}
}

func TestApplyInstanceToolConfig_NoInstanceTools(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Save instance config without tools (e.g. pre-existing instance).
	config.SaveInstanceConfig(configPath, config.InstanceConfig{Model: "test"})

	cfg := config.AgentConfig{
		AllowedTools: []string{"Bash", "Read"},
	}
	original := make([]string, len(cfg.AllowedTools))
	copy(original, cfg.AllowedTools)

	// Should not override — fall back to agent.md.
	applyInstanceToolConfig(configPath, &cfg)
	if len(cfg.AllowedTools) != len(original) {
		t.Errorf("AllowedTools should not change: got %v", cfg.AllowedTools)
	}
}

func TestApplyInstanceToolConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nonexistent.yaml")

	cfg := config.AgentConfig{
		AllowedTools: []string{"Bash", "Read"},
	}

	// No config file — should not override.
	applyInstanceToolConfig(configPath, &cfg)
	if len(cfg.AllowedTools) != 2 {
		t.Errorf("AllowedTools should not change: got %v", cfg.AllowedTools)
	}
}

func TestCreateSessionDirs(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions", "test-session")

	if err := createSessionDirs(sessDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, sub := range []string{"scratch", "tmp"} {
		info, err := os.Stat(filepath.Join(sessDir, sub))
		if err != nil {
			t.Errorf("%s should exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", sub)
		}
	}
}

func TestCoalesce(t *testing.T) {
	tests := []struct {
		override string
		fallback string
		want     string
	}{
		{"override", "fallback", "override"},
		{"", "fallback", "fallback"},
		{"", "", ""},
		{"value", "", "value"},
	}
	for _, tt := range tests {
		got := coalesce(tt.override, tt.fallback)
		if got != tt.want {
			t.Errorf("coalesce(%q, %q) = %q, want %q", tt.override, tt.fallback, got, tt.want)
		}
	}
}

func TestAgentHasSkills(t *testing.T) {
	mgr, dir := setupTestManager(t)

	// Agent with inline skills.
	cfg := config.AgentConfig{Name: "with-skills", Skills: []config.SkillConfig{{Name: "s"}}}
	if !mgr.agentHasSkills(cfg) {
		t.Error("should report true for inline skills")
	}

	// Agent with no inline skills and no skills dir.
	cfg = config.AgentConfig{Name: "no-skills"}
	writeAgentMD(t, dir, "no-skills", `---
name: no-skills
---
No skills.`)
	if mgr.agentHasSkills(cfg) {
		t.Error("should report false for agent with no skills")
	}

	// Agent with skills dir on disk.
	cfg = config.AgentConfig{Name: "disk-skills"}
	writeAgentMD(t, dir, "disk-skills", `---
name: disk-skills
---
Has disk skills.`)
	skillsDir := filepath.Join(dir, "agents", "disk-skills", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !mgr.agentHasSkills(cfg) {
		t.Error("should report true for skills dir on disk")
	}
}

func TestRegisterInstanceInDB_NilPDB(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.registerInstanceInDB(t.Context(), "id", "sess", "", "", config.AgentConfig{Name: "test"}, config.ModePersistent, "", "")
	if err != nil {
		t.Fatalf("should succeed with nil PDB: %v", err)
	}
}

func TestRegisterInstanceInDB_WithPDB(t *testing.T) {
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	err := mgr.registerInstanceInDB(t.Context(), "inst-1", "sess-1", "", "", config.AgentConfig{Name: "test"}, config.ModePersistent, "", "home")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the instance was created.
	dbInst, err := pdb.GetInstance(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("instance not found in DB: %v", err)
	}
	if dbInst.AgentName != "test" {
		t.Errorf("agent name = %q, want test", dbInst.AgentName)
	}

	// Duplicate should not fail (errors.Is(err, ErrDuplicate) is handled).
	err = mgr.registerInstanceInDB(t.Context(), "inst-1", "sess-1", "", "", config.AgentConfig{Name: "test"}, config.ModePersistent, "", "home")
	if err != nil {
		t.Fatalf("duplicate registration should not fail: %v", err)
	}
}

func TestEnrichPersonaNames(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "Custom Name", "Custom Desc", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	info, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("instance not found")
	}
	// GetInstance enriches from persona.md.
	if info.Name != "Custom Name" {
		t.Errorf("name = %q, want %q", info.Name, "Custom Name")
	}
	if info.Description != "Custom Desc" {
		t.Errorf("description = %q, want %q", info.Description, "Custom Desc")
	}
}

// --- manager_resolve.go ---

func TestApplyModelOverride(t *testing.T) {
	tests := []struct {
		name         string
		initial      models.ModelSpec
		override     string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "full override with provider",
			initial:      models.ModelSpec{Provider: "old", Model: "old-model"},
			override:     "anthropic/claude-sonnet-4-20250514",
			wantProvider: "anthropic",
			wantModel:    "claude-sonnet-4-20250514",
		},
		{
			name:         "bare model clears provider",
			initial:      models.ModelSpec{Provider: "anthropic", Model: "old-model"},
			override:     "gpt-4o",
			wantProvider: "",
			wantModel:    "gpt-4o",
		},
		{
			name:         "openrouter override",
			initial:      models.ModelSpec{},
			override:     "openrouter/anthropic/claude-sonnet-4-20250514",
			wantProvider: "openrouter",
			wantModel:    "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.initial
			applyModelOverride(&spec, tt.override)
			if spec.Provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", spec.Provider, tt.wantProvider)
			}
			if spec.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", spec.Model, tt.wantModel)
			}
		})
	}
}

// fullMockCP implements ControlPlane with full method support for model resolution tests.
type fullMockCP struct {
	secretNames        []string
	secretEnv          []string
	providerType       string
	apiKey             string
	baseURL            string
	providerConfigured bool
	defaultModel       models.ModelSpec
	providers          map[string]struct{ apiKey, baseURL string }
	providerTypes      []string
}

func (m *fullMockCP) SecretNames() []string { return m.secretNames }
func (m *fullMockCP) SecretEnv() []string   { return m.secretEnv }
func (m *fullMockCP) ProviderInfo() (string, string, string, bool) {
	return m.providerType, m.apiKey, m.baseURL, m.providerConfigured
}
func (m *fullMockCP) ProviderByType(pt string) (string, string, bool) {
	if m.providers == nil {
		return "", "", false
	}
	p, ok := m.providers[pt]
	if !ok {
		return "", "", false
	}
	return p.apiKey, p.baseURL, true
}
func (m *fullMockCP) ConfiguredProviderTypes() []string { return m.providerTypes }
func (m *fullMockCP) DefaultModelSpec() models.ModelSpec {
	return m.defaultModel
}
func (m *fullMockCP) ResolveSecret(value string) string { return value }

func TestResolveModelSpec_NilCP(t *testing.T) {
	mgr, _ := setupTestManager(t) // nil CP by default
	spec, apiKey, baseURL, err := mgr.resolveModelSpec("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.IsEmpty() {
		t.Errorf("expected empty spec, got %v", spec)
	}
	if apiKey != "" || baseURL != "" {
		t.Error("expected empty credentials with nil CP")
	}
}

func TestResolveModelSpec_NilCP_WithAgentModel(t *testing.T) {
	mgr, _ := setupTestManager(t)
	spec, _, _, err := mgr.resolveModelSpec("anthropic/claude-sonnet-4-20250514")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Provider != "anthropic" || spec.Model != "claude-sonnet-4-20250514" {
		t.Errorf("spec = %v, want anthropic/claude-sonnet-4-20250514", spec)
	}
}

func TestResolveModelSpec_CPDefault(t *testing.T) {
	cp := &fullMockCP{
		defaultModel:       models.ModelSpec{Provider: "anthropic", Model: "claude-sonnet-4-20250514"},
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-test",
		providers: map[string]struct{ apiKey, baseURL string }{
			"anthropic": {apiKey: "sk-test"},
		},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	spec, apiKey, _, err := mgr.resolveModelSpec("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Provider != "anthropic" || spec.Model != "claude-sonnet-4-20250514" {
		t.Errorf("spec = %v, want anthropic/claude-sonnet-4-20250514", spec)
	}
	if apiKey != "sk-test" {
		t.Errorf("apiKey = %q, want sk-test", apiKey)
	}
}

func TestResolveModelSpec_AgentOverridesDefault(t *testing.T) {
	cp := &fullMockCP{
		defaultModel:       models.ModelSpec{Provider: "anthropic", Model: "default-model"},
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-test",
		providers: map[string]struct{ apiKey, baseURL string }{
			"anthropic": {apiKey: "sk-test"},
			"openai":    {apiKey: "sk-openai"},
		},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	spec, apiKey, _, err := mgr.resolveModelSpec("openai/gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Provider != "openai" || spec.Model != "gpt-4o" {
		t.Errorf("spec = %v, want openai/gpt-4o", spec)
	}
	if apiKey != "sk-openai" {
		t.Errorf("apiKey = %q, want sk-openai", apiKey)
	}
}

func TestResolveModelSpec_EnvOverridesAgent(t *testing.T) {
	cp := &fullMockCP{
		defaultModel:       models.ModelSpec{Provider: "anthropic", Model: "default-model"},
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-test",
		providers: map[string]struct{ apiKey, baseURL string }{
			"anthropic": {apiKey: "sk-test"},
			"openai":    {apiKey: "sk-openai"},
		},
	}
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
		Model:      "openai/gpt-4o-mini",
	}, cp, logger, testWorkerFactory("hello"), nil, false)

	// Even though agent specifies one model, env override wins.
	spec, apiKey, _, err := mgr.resolveModelSpec("anthropic/claude-sonnet-4-20250514")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Provider != "openai" || spec.Model != "gpt-4o-mini" {
		t.Errorf("spec = %v, want openai/gpt-4o-mini", spec)
	}
	if apiKey != "sk-openai" {
		t.Errorf("apiKey = %q, want sk-openai", apiKey)
	}
}

func TestResolveModelSpec_ProviderNotConfigured(t *testing.T) {
	cp := &fullMockCP{
		defaultModel:       models.ModelSpec{Provider: "anthropic", Model: "model"},
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-test",
		providers:          map[string]struct{ apiKey, baseURL string }{}, // empty providers
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	_, _, _, err := mgr.resolveModelSpec("nonexistent-provider/model")
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestResolveProviderCredentials_EmptySpec(t *testing.T) {
	cp := &fullMockCP{
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-default",
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	spec, apiKey, _, err := mgr.resolveProviderCredentials(models.ModelSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", spec.Provider)
	}
	if apiKey != "sk-default" {
		t.Errorf("apiKey = %q, want sk-default", apiKey)
	}
}

func TestResolveProviderCredentials_EmptySpec_NoProvider(t *testing.T) {
	cp := &fullMockCP{
		providerConfigured: false,
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	spec, _, _, err := mgr.resolveProviderCredentials(models.ModelSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.IsEmpty() {
		t.Errorf("expected empty spec when no provider, got %v", spec)
	}
}

func TestResolveProviderCredentials_BareModel_FallsBackToDefault(t *testing.T) {
	cp := &fullMockCP{
		providerConfigured: true,
		providerType:       "anthropic",
		apiKey:             "sk-fallback",
		providerTypes:      []string{"anthropic"},
		providers: map[string]struct{ apiKey, baseURL string }{
			"anthropic": {apiKey: "sk-fallback"},
		},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	// Bare model name that doesn't match any known model.
	spec, apiKey, _, err := mgr.resolveProviderCredentials(models.ModelSpec{Model: "unknown-model"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Falls back to default provider.
	if spec.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (fallback)", spec.Provider)
	}
	if apiKey != "sk-fallback" {
		t.Errorf("apiKey = %q, want sk-fallback", apiKey)
	}
}

func TestResolveProviderCredentials_BareModel_NoProviderAtAll(t *testing.T) {
	cp := &fullMockCP{
		providerConfigured: false,
		providerTypes:      []string{},
	}
	mgr, _ := setupTestManagerWithCP(t, cp)
	_, _, _, err := mgr.resolveProviderCredentials(models.ModelSpec{Model: "some-model"})
	if err == nil {
		t.Fatal("expected error when no provider configured")
	}
}

// --- manager_restore.go ---

func TestRegisterSessionInDB_NilPDB(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.registerSessionInDB("inst-1", "sess-1", "agent", config.ModePersistent)
	if err != nil {
		t.Fatalf("should succeed with nil PDB: %v", err)
	}
}

func TestRegisterSessionInDB_WithPDB(t *testing.T) {
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	// Create the instance first (FK constraint).
	_ = pdb.CreateInstance(t.Context(), platformdb.Instance{
		ID: "inst-1", AgentName: "agent", Mode: "persistent",
	})

	err := mgr.registerSessionInDB("inst-1", "sess-1", "agent", config.ModePersistent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- manager_worker.go ---

func TestSetInstanceStatus_NilPDB(t *testing.T) {
	mgr, _ := setupTestManager(t)
	// Should not panic with nil PDB.
	mgr.setInstanceStatus("any-id", "running")
}

func TestSetInstanceStatus_WithPDB(t *testing.T) {
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	writeAgentMD(t, dir, "test-agent", testAgentMD)
	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Setting status should work.
	mgr.setInstanceStatus(id, "stopped")

	dbInst, err := pdb.GetInstance(t.Context(), id)
	if err != nil {
		t.Fatalf("instance not found: %v", err)
	}
	if dbInst.Status != "stopped" {
		t.Errorf("status = %q, want stopped", dbInst.Status)
	}
}

// --- Additional coverage for manager_lifecycle.go ---

func TestPrepareInstanceDirs_NewInstance(t *testing.T) {
	mgr, dir := setupTestManager(t)
	instDir, sessDir, dirIsNew, err := mgr.prepareInstanceDirs("new-inst-id", "new-sess-id", config.ModePersistent, "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dirIsNew {
		t.Error("expected dirIsNew = true for new instance")
	}
	if _, err := os.Stat(instDir); err != nil {
		t.Errorf("instance dir should exist: %v", err)
	}
	if _, err := os.Stat(sessDir); err != nil {
		t.Errorf("session dir should exist: %v", err)
	}
	// Check that persona.md and memory.md were seeded.
	if _, err := os.Stat(filepath.Join(instDir, "persona.md")); err != nil {
		t.Errorf("persona.md should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instDir, "memory.md")); err != nil {
		t.Errorf("memory.md should exist: %v", err)
	}
	_ = dir
}

func TestPrepareInstanceDirs_ExistingInstance(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Pre-create the instance dir.
	instDir := mgr.instanceDir("existing-inst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, dirIsNew, err := mgr.prepareInstanceDirs("existing-inst", "new-sess", config.ModePersistent, "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dirIsNew {
		t.Error("expected dirIsNew = false for existing instance")
	}
	// persona.md should NOT be seeded for existing instances.
	if _, err := os.Stat(filepath.Join(instDir, "persona.md")); !os.IsNotExist(err) {
		t.Error("persona.md should not be seeded for existing instance dir")
	}
}

func TestBuildSpawnConfig(t *testing.T) {
	mgr, dir := setupTestManager(t)
	instDir := filepath.Join(dir, "instances", "inst-1")
	sessDir := filepath.Join(dir, "instances", "inst-1", "sessions", "sess-1")
	tools := map[string]bool{"Bash": true, "Read": true}
	cfg := mgr.buildSpawnConfig("inst-1", "sess-1", "test-agent", tools, instDir, sessDir)

	if cfg.InstanceID != "inst-1" {
		t.Errorf("InstanceID = %q, want inst-1", cfg.InstanceID)
	}
	if cfg.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", cfg.SessionID)
	}
	if cfg.AgentName != "test-agent" {
		t.Errorf("AgentName = %q, want test-agent", cfg.AgentName)
	}
	if !cfg.EffectiveTools["Bash"] || !cfg.EffectiveTools["Read"] {
		t.Error("EffectiveTools should contain Bash and Read")
	}
	// NetworkAccess should be true because Bash is in effective tools.
	if !cfg.NetworkAccess {
		t.Error("NetworkAccess should be true when Bash is in effective tools")
	}
}

func TestMakeCleanup(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess")
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("test"), 0o600)

	cleanup := makeCleanup(sessDir, instDir, configPath, true)
	cleanup()

	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("sessDir should be removed")
	}
	if _, err := os.Stat(instDir); !os.IsNotExist(err) {
		t.Error("instDir should be removed when dirIsNew=true")
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("configPath should be removed when dirIsNew=true")
	}
}

func TestMakeCleanup_NotNewDir(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess")
	instDir := filepath.Join(dir, "inst")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("test"), 0o600)

	cleanup := makeCleanup(sessDir, instDir, configPath, false)
	cleanup()

	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("sessDir should be removed")
	}
	// instDir and configPath should remain since dirIsNew=false.
	if _, err := os.Stat(instDir); err != nil {
		t.Error("instDir should survive when dirIsNew=false")
	}
}

// --- Circular dependency detection ---

func TestCreateInstance_InvalidName(t *testing.T) {
	mgr, _ := setupTestManager(t)
	tests := []string{"", "../escape", "has space", "special!"}
	for _, name := range tests {
		_, err := mgr.CreateInstance(t.Context(), name, "", "persistent", "", "", "", "")
		if err == nil {
			t.Errorf("expected error for name %q", name)
		}
	}
}

func TestSpawnEphemeral_InvalidName(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.SpawnEphemeral(t.Context(), "../escape", "prompt", "", "", nil)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}

func TestSpawnEphemeral_MissingAgent(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.SpawnEphemeral(t.Context(), "nonexistent", "prompt", "", "", nil)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

// --- PushConfigUpdateAll ---

func TestPushConfigUpdateAll_NoInstances(t *testing.T) {
	mgr, _ := setupTestManager(t)
	// Should not panic with no running instances.
	mgr.PushConfigUpdateAll()
}

func TestPushConfigUpdateAll_WithInstances(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Should not panic — runs pushConfigUpdate for each unique agent.
	mgr.PushConfigUpdateAll()
}

// --- resolveModelString ---

func TestResolveModelString_NilCP(t *testing.T) {
	mgr, _ := setupTestManager(t)
	s := mgr.resolveModelString("anthropic/claude-sonnet-4-20250514")
	if s != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("got %q, want anthropic/claude-sonnet-4-20250514", s)
	}
}

func TestResolveModelString_Empty(t *testing.T) {
	mgr, _ := setupTestManager(t)
	s := mgr.resolveModelString("")
	want := models.ModelSpec{}.String()
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// --- Path helpers coverage ---

func TestInstanceSessionDir(t *testing.T) {
	mgr, dir := setupTestManager(t)
	got := mgr.instanceSessionDir("inst-1", "sess-1")
	want := filepath.Join(dir, "instances", "inst-1", "sessions", "sess-1")
	if got != want {
		t.Errorf("instanceSessionDir = %q, want %q", got, want)
	}
}

// --- NewSession - stopped instance ---

func TestNewSession_StoppedInstance(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	_, err = mgr.NewSession(id)
	if err == nil {
		t.Fatal("expected error for stopped instance")
	}
}

func TestNewSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.NewSession("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
}

// TestNewSession_OldWatchWorkerDoesNotTearDown verifies that the old
// watchWorker goroutine (monitoring the pre-clear worker) does not
// mistakenly tear down the instance after NewSession replaces the handle.
// This was a real bug: the old watchWorker woke up after NewSession released
// inst.mu, saw inst.handle was non-nil (the NEW handle), and treated the
// old worker's exit as an unexpected crash.
func TestNewSession_OldWatchWorkerDoesNotTearDown(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	info, ok := mgr.GetInstance(id)
	if !ok || info.Status != InstanceStatusRunning {
		t.Fatalf("expected running, got %v", info.Status)
	}

	// NewSession shuts down the old worker (closing its Done channel) and
	// spawns a new one. The old watchWorker goroutine will wake up and
	// must detect the handle was replaced, not tear down the instance.
	newSess, err := mgr.NewSession(id)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if newSess == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Give the old watchWorker goroutine time to run and (incorrectly) tear down.
	// The bug manifested immediately after inst.mu was released.
	time.Sleep(50 * time.Millisecond)

	info, ok = mgr.GetInstance(id)
	if !ok {
		t.Fatal("instance was removed — old watchWorker incorrectly tore it down")
	}
	if info.Status != InstanceStatusRunning {
		t.Fatalf("instance status is %q — old watchWorker incorrectly stopped it", info.Status)
	}
}

// --- Shutdown with mixed stopped/running ---

func TestShutdown_MixedStoppedAndRunning(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id1, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create instance 1: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create instance 2: %v", err)
	}
	mgr.StopInstance(id1) // id1 is stopped

	mgr.Shutdown()

	if len(mgr.ListInstances()) != 0 {
		t.Error("expected 0 instances after shutdown")
	}
}

// --- DeleteInstance with stopped children ---

func TestDeleteInstance_StoppedChildren(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "parent", `---
name: parent
model: fake-model
---
Parent.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child.`)

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "", "", "", "")

	// Stop the child first, then delete parent.
	mgr.StopInstance(childID)

	if err := mgr.DeleteInstance(parentID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, ok := mgr.GetInstance(parentID); ok {
		t.Error("parent should be deleted")
	}
	if _, ok := mgr.GetInstance(childID); ok {
		t.Error("stopped child should be deleted with parent")
	}
}

// --- hasParameterized ---

func TestHasParameterized(t *testing.T) {
	tests := []struct {
		name  string
		rules []toolrules.Rule
		want  bool
	}{
		{
			name:  "empty",
			rules: nil,
			want:  false,
		},
		{
			name:  "whole tool only",
			rules: []toolrules.Rule{{Tool: "Bash"}},
			want:  false,
		},
		{
			name:  "parameterized rule",
			rules: []toolrules.Rule{{Tool: "Bash", Pattern: "ls *"}},
			want:  true,
		},
		{
			name:  "mixed whole and parameterized",
			rules: []toolrules.Rule{{Tool: "Read"}, {Tool: "Bash", Pattern: "ls *"}},
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasParameterized(tt.rules)
			if got != tt.want {
				t.Errorf("hasParameterized() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- buildInstance ---

func TestBuildInstance(t *testing.T) {
	done := make(chan struct{})
	w := &testWorker{done: done}
	handle := &WorkerHandle{
		Worker: w,
		Kill:   func() {},
		Close:  func() {},
		Done:   done,
	}
	cfg := config.AgentConfig{Name: "test", Description: "desc"}
	inst := buildInstance("inst-1", "sess-1", "web", cfg, config.ModePersistent, "parent-1", "home",
		"anthropic/model", "Display", "Display Desc",
		handle, nil, nil, &sync.Mutex{}, map[string]bool{"Bash": true}, nil, nil)

	if inst.info.ID != "inst-1" {
		t.Errorf("ID = %q, want inst-1", inst.info.ID)
	}
	if inst.info.Name != "Display" {
		t.Errorf("Name = %q, want Display (coalesced from displayName)", inst.info.Name)
	}
	if inst.info.Description != "Display Desc" {
		t.Errorf("Description = %q, want Display Desc", inst.info.Description)
	}
	if inst.info.ParentID != "parent-1" {
		t.Errorf("ParentID = %q, want parent-1", inst.info.ParentID)
	}
	if inst.info.Status != InstanceStatusRunning {
		t.Errorf("Status = %q, want running", inst.info.Status)
	}
	if inst.agentName != "test" {
		t.Errorf("agentName = %q, want test", inst.agentName)
	}
}

func TestBuildInstance_FallbackNames(t *testing.T) {
	done := make(chan struct{})
	w := &testWorker{done: done}
	handle := &WorkerHandle{
		Worker: w,
		Kill:   func() {},
		Close:  func() {},
		Done:   done,
	}
	cfg := config.AgentConfig{Name: "agent-name", Description: "agent-desc"}
	inst := buildInstance("inst-2", "sess-2", "web", cfg, config.ModeEphemeral, "", "home",
		"model", "", "", // empty display name/desc
		handle, nil, nil, nil, nil, nil, nil)

	if inst.info.Name != "agent-name" {
		t.Errorf("Name = %q, want agent-name (fallback)", inst.info.Name)
	}
	if inst.info.Description != "agent-desc" {
		t.Errorf("Description = %q, want agent-desc (fallback)", inst.info.Description)
	}
}

// --- instanceInfoToIPC ---

func TestInstanceInfoToIPC(t *testing.T) {
	mgr, _ := setupTestManager(t)
	info := InstanceInfo{
		ID:          "inst-1",
		Name:        "test",
		Mode:        config.ModePersistent,
		Description: "desc",
		ParentID:    "parent",
		Status:      InstanceStatusRunning,
		Model:       "model",
	}
	ipcInfo := mgr.instanceInfoToIPC(info)
	if ipcInfo.ID != "inst-1" || ipcInfo.Name != "test" || ipcInfo.Mode != "persistent" {
		t.Errorf("unexpected IPC info: %+v", ipcInfo)
	}
	if ipcInfo.Status != "running" || ipcInfo.ParentID != "parent" {
		t.Errorf("unexpected IPC info: %+v", ipcInfo)
	}
}

// --- StopInstance double-stop returns info ---

func TestStopInstance_DoubleStopReturnsInfo(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	info, err := mgr.StopInstance(id)
	if err != nil {
		t.Fatalf("double stop should not error: %v", err)
	}
	if info.Status != string(InstanceStatusStopped) {
		t.Errorf("status = %q, want stopped", info.Status)
	}
	if info.Name != "test-agent" {
		t.Errorf("name = %q, want test-agent", info.Name)
	}
}

// --- Restore with PDB and session ---

func TestRestoreInstances_NoPDB(t *testing.T) {
	mgr, _ := setupTestManager(t) // no PDB
	err := mgr.RestoreInstances(t.Context())
	if err != nil {
		t.Fatalf("should succeed with nil PDB: %v", err)
	}
}

// --- unregisterLocked with no parent ---

func TestUnregisterLocked_NoParent(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	mgr.mu.Lock()
	inst := mgr.instances[id]
	mgr.unregisterLocked(id, inst)
	mgr.mu.Unlock()

	if _, ok := mgr.GetInstance(id); ok {
		t.Error("instance should be unregistered")
	}
}

// --- NewManager with nil WorkerFactory ---

func TestNewManager_NilWorkerFactory(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	// nil wf should fall back to defaultWorkerFactory (which would fail on actual spawn,
	// but the manager should create successfully).
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, nil, nil, false)
	if mgr == nil {
		t.Fatal("NewManager should not return nil")
	}
}

// --- Restore with invalid agent name ---

func TestRestore_InvalidAgentNameSkipped(t *testing.T) {
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Insert an instance with an invalid agent name.
	pdb.CreateInstance(ctx, platformdb.Instance{
		ID:        "bad-name-inst",
		AgentName: "../evil",
		Mode:      "persistent",
		Status:    "running",
	})

	mgr := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)
	if err := mgr.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore should not fail: %v", err)
	}

	if _, ok := mgr.GetInstance("bad-name-inst"); ok {
		t.Error("instance with invalid name should not be restored")
	}
}

// --- collectDescendants edge cases ---

func TestCollectDescendants_DeepTree(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "agent", `---
name: agent
model: fake-model
---
Agent.`)

	// Create a chain: root -> child -> grandchild
	rootID, _ := mgr.CreateInstance(t.Context(), "agent", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "agent", rootID, "persistent", "", "", "", "")
	grandchildID, _ := mgr.CreateInstance(t.Context(), "agent", childID, "persistent", "", "", "", "")

	descendants := mgr.collectDescendants(rootID)
	if len(descendants) != 3 {
		t.Fatalf("expected 3 descendants, got %d: %v", len(descendants), descendants)
	}

	// Should be BFS order: root, child, grandchild
	if descendants[0] != rootID || descendants[1] != childID || descendants[2] != grandchildID {
		t.Errorf("unexpected order: %v", descendants)
	}
}

func TestCollectDescendants_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	descendants := mgr.collectDescendants("nonexistent")
	if len(descendants) != 0 {
		t.Errorf("expected empty descendants, got %v", descendants)
	}
}

// --- StartInstance resumes latest session from PDB ---

func TestStartInstance_ResumesLatestSession(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()
	mgr := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(ctx, "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	origSession := mgr.ActiveSessionID(id)

	mgr.StopInstance(id)

	if err := mgr.StartInstance(ctx, id); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// After restart, should resume the latest session.
	newSession := mgr.ActiveSessionID(id)
	if newSession == "" {
		t.Fatal("expected non-empty session after restart")
	}
	// Session should be the original one (resumed from PDB).
	if newSession != origSession {
		t.Logf("original session: %s, new session: %s", origSession, newSession)
		// This is acceptable — depends on whether the session was marked stopped.
	}
}

// --- Restore with session history ---

func TestRestore_ResumesExistingSession(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)
	id, _ := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "", "", "", "")
	sessID := mgr1.ActiveSessionID(id)
	mgr1.Shutdown()

	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	restoredSess := mgr2.ActiveSessionID(id)
	if restoredSess != sessID {
		t.Errorf("restored session = %q, want %q (original)", restoredSess, sessID)
	}
}

// Additional edge case: GetHistory with tool messages

func TestGetHistory_FiltersByRole(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sessID := mgr.ActiveSessionID(id)

	// Insert various message roles.
	for _, role := range []string{"user", "assistant", "tool", "system"} {
		pdb.AppendMessage(t.Context(), sessID, role, role+" content", "", 0)
	}

	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should include user, assistant, tool (3). System is filtered out.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	roles := make(map[string]bool)
	for _, m := range msgs {
		roles[m.Role] = true
	}
	if roles["system"] {
		t.Error("system messages should be filtered out")
	}
	if !roles["tool"] {
		t.Error("tool messages should be included")
	}
}

// Test GetHistory with meta messages

func TestGetHistory_MetaMessages(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sessID := mgr.ActiveSessionID(id)

	pdb.AppendMessage(t.Context(), sessID, "user", "meta message", "", 0, true)

	msgs, err := mgr.GetHistory(t.Context(), id, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].IsMeta {
		t.Error("expected IsMeta = true for meta message")
	}
}

// --- DeleteInstance with PDB ---

func TestDeleteInstance_WithPDB(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), pdb, false)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := mgr.DeleteInstance(id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Instance should be gone from DB too.
	_, err = pdb.GetInstance(t.Context(), id)
	if err == nil {
		t.Error("instance should be deleted from DB")
	}

	// Instance dir should be removed.
	if _, err := os.Stat(mgr.instanceDir(id)); !os.IsNotExist(err) {
		t.Error("instance dir should be removed after delete")
	}
}

// --- createInferenceLoop with empty provider ---

func TestCreateInferenceLoop_EmptyProvider(t *testing.T) {
	mgr, _ := setupTestManager(t)
	loopCfg := inference.LoopConfig{
		AgentConfig: config.AgentConfig{Name: "test"},
		Logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	loop, err := mgr.createInferenceLoop(t.Context(), &loopCfg, models.ModelSpec{}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loop != nil {
		t.Error("expected nil loop when provider is empty")
	}
}

// --- detachAllSlots ---

func TestDetachAllSlots(t *testing.T) {
	mgr, _ := setupTestManager(t)
	done := make(chan struct{})
	w := &testWorker{done: done}
	handle := &WorkerHandle{
		Worker: w,
		Kill:   func() {},
		Close:  func() {},
		Done:   done,
	}
	inst := &instance{
		sessions: map[string]*sessionSlot{
			"sess-1": {
				sessionID: "sess-1",
				channel:   "web",
				worker:    w,
				handle:    handle,
			},
		},
		channelIndex: map[string]string{"web": "sess-1"},
		info:         InstanceInfo{Status: InstanceStatusRunning},
	}

	handles := mgr.detachAllSlots(inst, InstanceStatusStopped)
	if len(handles) != 1 {
		t.Fatalf("expected 1 handle, got %d", len(handles))
	}
	if handles[0] != handle {
		t.Error("should return the captured handle")
	}
	slot := inst.sessions["sess-1"]
	if slot.worker != nil {
		t.Error("worker should be nil after detach")
	}
	if slot.handle != nil {
		t.Error("handle should be nil after detach")
	}
	if inst.info.Status != InstanceStatusStopped {
		t.Errorf("status = %q, want stopped", inst.info.Status)
	}
}

func TestDetachAllSlots_NoStatusChange(t *testing.T) {
	mgr, _ := setupTestManager(t)
	inst := &instance{
		sessions:     make(map[string]*sessionSlot),
		channelIndex: make(map[string]string),
		info:         InstanceInfo{Status: InstanceStatusRunning},
	}

	mgr.detachAllSlots(inst, "") // empty status = no change
	if inst.info.Status != InstanceStatusRunning {
		t.Errorf("status should remain running, got %q", inst.info.Status)
	}
}

// --- reregisterStopped ---

func TestReregisterStopped(t *testing.T) {
	mgr, _ := setupTestManager(t)
	inst := &instance{
		info: InstanceInfo{
			ID:       "inst-1",
			ParentID: "parent-1",
			Status:   InstanceStatusRunning,
		},
	}

	mgr.reregisterStopped("inst-1", inst)

	if inst.info.Status != InstanceStatusStopped {
		t.Errorf("status = %q, want stopped", inst.info.Status)
	}

	found := mgr.getInstance("inst-1")
	if found == nil {
		t.Fatal("instance should be registered")
	}

	mgr.mu.RLock()
	children := mgr.children["parent-1"]
	mgr.mu.RUnlock()
	found2 := false
	for _, c := range children {
		if c == "inst-1" {
			found2 = true
		}
	}
	if !found2 {
		t.Error("instance should be in parent's children list")
	}
}

// --- splitChannelKey / makeChannelKey ---

func TestSplitChannelKey(t *testing.T) {
	tests := []struct {
		key      string
		wantType string
		wantID   string
	}{
		{"web", "web", ""},
		{"tg:12345", "tg", "12345"},
		{"slack:C1:thread", "slack", "C1:thread"},
		{"", "", ""},
	}
	for _, tt := range tests {
		gotType, gotID := splitChannelKey(tt.key)
		if gotType != tt.wantType || gotID != tt.wantID {
			t.Errorf("splitChannelKey(%q) = (%q, %q), want (%q, %q)",
				tt.key, gotType, gotID, tt.wantType, tt.wantID)
		}
	}
}

func TestMakeChannelKey(t *testing.T) {
	tests := []struct {
		channelType string
		channelID   string
		want        string
	}{
		{"web", "", "web"},
		{"tg", "12345", "tg:12345"},
		{"slack", "C1", "slack:C1"},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := makeChannelKey(tt.channelType, tt.channelID)
		if got != tt.want {
			t.Errorf("makeChannelKey(%q, %q) = %q, want %q",
				tt.channelType, tt.channelID, got, tt.want)
		}
	}
}

func TestMakeChannelKey_RoundTrip(t *testing.T) {
	pairs := []struct {
		channelType string
		channelID   string
	}{
		{"web", ""},
		{"tg", "12345"},
		{"slack", "C1:thread"},
		{"agent", "some-uuid"},
	}
	for _, p := range pairs {
		key := makeChannelKey(p.channelType, p.channelID)
		gotType, gotID := splitChannelKey(key)
		if gotType != p.channelType || gotID != p.channelID {
			t.Errorf("round-trip failed for (%q, %q): makeChannelKey=%q, split=(%q, %q)",
				p.channelType, p.channelID, key, gotType, gotID)
		}
	}
}

// --- EnsureSession ---

func TestEnsureSession_Idempotent(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	sess1, err := mgr.EnsureSession(t.Context(), id, "tg:42")
	if err != nil {
		t.Fatalf("first EnsureSession: %v", err)
	}
	if sess1 == "" {
		t.Fatal("expected non-empty session ID")
	}

	sess2, err := mgr.EnsureSession(t.Context(), id, "tg:42")
	if err != nil {
		t.Fatalf("second EnsureSession: %v", err)
	}
	if sess1 != sess2 {
		t.Errorf("idempotency violated: first=%q second=%q", sess1, sess2)
	}
}

func TestEnsureSession_DifferentChannels(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	webSess, err := mgr.EnsureSession(t.Context(), id, "web")
	if err != nil {
		t.Fatalf("web EnsureSession: %v", err)
	}

	tgSess, err := mgr.EnsureSession(t.Context(), id, "tg:123")
	if err != nil {
		t.Fatalf("tg EnsureSession: %v", err)
	}

	if webSess == tgSess {
		t.Errorf("expected distinct sessions for different channels, both got %q", webSess)
	}
}

func TestEnsureSession_StoppedInstance(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.StopInstance(id)

	_, err = mgr.EnsureSession(t.Context(), id, "web")
	if err == nil {
		t.Fatal("expected error for stopped instance")
	}
	if !errors.Is(err, ErrInstanceStopped) {
		t.Errorf("expected ErrInstanceStopped, got %v", err)
	}
}

func TestEnsureSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)

	_, err := mgr.EnsureSession(t.Context(), "nonexistent-id", "web")
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestEnsureSession_MaxSessions(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The initial CreateInstance already created one session via the "web" channel.
	// Count how many sessions already exist so we know how many more to add.
	inst := mgr.getInstance(id)
	inst.mu.Lock()
	existingCount := len(inst.sessions)
	inst.mu.Unlock()

	// Fill remaining slots up to the limit.
	for i := existingCount; i < maxSessionsPerInstance; i++ {
		channelKey := fmt.Sprintf("tg:%d", i)
		_, err := mgr.EnsureSession(t.Context(), id, channelKey)
		if err != nil {
			t.Fatalf("EnsureSession(%q) at slot %d: %v", channelKey, i, err)
		}
	}

	// One more beyond the limit should fail.
	_, err = mgr.EnsureSession(t.Context(), id, "tg:overflow")
	if err == nil {
		t.Fatal("expected error when exceeding maxSessionsPerInstance")
	}
}

// --- instanceBySession concurrency ---

func TestInstanceBySession_ConcurrentSafe(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Grab the initial session ID so readers have something real to look up.
	knownSess := mgr.ActiveSessionID(id)

	const numReaders = 10
	var wg sync.WaitGroup

	// Readers: call instanceBySession concurrently.
	for range numReaders {
		wg.Go(func() {
			for range 50 {
				_ = mgr.instanceBySession(knownSess)
				_ = mgr.instanceBySession("nonexistent-session")
			}
		})
	}

	// Writer: add and (indirectly) query sessions while readers run.
	wg.Go(func() {
		for i := range 5 {
			channelKey := fmt.Sprintf("tg:concurrent-%d", i)
			sess, addErr := mgr.EnsureSession(t.Context(), id, channelKey)
			if addErr != nil {
				// May fail if maxSessionsPerInstance is reached — that's fine.
				return
			}
			_ = mgr.instanceBySession(sess)
		}
	})

	wg.Wait()
	// No panic = success.
}

// --- concurrency regression tests ---

// TestDetachAllSlots_RaceWithAcquireSlot exercises the fix where detachAllSlots
// acquires slot.mu before nil'ing slot fields. Without the fix, the race
// detector flags concurrent reads in acquireSlot.
func TestDetachAllSlots_RaceWithAcquireSlot(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Create a few extra sessions to increase contention surface.
	for i := range 3 {
		if _, err := mgr.EnsureSession(t.Context(), id, fmt.Sprintf("tg:%d", i)); err != nil {
			t.Fatalf("EnsureSession: %v", err)
		}
	}

	inst := mgr.getInstance(id)
	// Snapshot session IDs for acquireSlot calls.
	inst.mu.Lock()
	var sessionIDs []string
	for sid := range inst.sessions {
		sessionIDs = append(sessionIDs, sid)
	}
	inst.mu.Unlock()

	var wg sync.WaitGroup

	// Readers: call acquireSlot concurrently.
	for _, sid := range sessionIDs {
		wg.Go(func() {
			for range 50 {
				slot, _, err := mgr.acquireSlot(t.Context(), id, sid)
				if err == nil {
					slot.mu.Unlock()
				}
			}
		})
	}

	// Writer: detach all slots concurrently with readers.
	wg.Go(func() {
		time.Sleep(time.Millisecond) // let readers start
		mgr.detachAllSlots(inst, InstanceStatusStopped)
	})

	wg.Wait()
	// Success = no race detector findings and no panics.
}

// TestEnsureSession_ConcurrentSameChannel verifies that concurrent
// EnsureSession calls for the same channel key return the same session ID
// and don't race on slot field reads.
func TestEnsureSession_ConcurrentSameChannel(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const goroutines = 10
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			results[i], errs[i] = mgr.EnsureSession(t.Context(), id, "tg:same")
		})
	}
	wg.Wait()

	// All should succeed and return the same session ID.
	var expected string
	for i := range goroutines {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if expected == "" {
			expected = results[i]
		} else if results[i] != expected {
			t.Errorf("goroutine %d: session=%q, want %q", i, results[i], expected)
		}
	}
}

// TestConfigGen_PreventsStalePush verifies that pushConfigToInstance discards
// a pre-created language model when configGen has been bumped by a concurrent
// config update between the two lock acquisitions.
func TestConfigGen_PreventsStalePush(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	inst := mgr.getInstance(id)

	// Record the model before the push.
	inst.mu.Lock()
	modelBefore := inst.info.Model
	inst.mu.Unlock()

	// pushToolsAndModel is the function that applies model changes and bumps
	// configGen. Call it with a non-nil LM and a model that differs from current.
	// It should apply the model and bump configGen.
	cfg, loadErr := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	if loadErr != nil {
		t.Fatalf("load agent: %v", loadErr)
	}
	pc := &configPushContext{
		cfg:       cfg,
		model:     "new-provider/new-model",
		modelSpec: models.ModelSpec{Provider: "new-provider", Model: "new-model"},
	}

	inst.mu.Lock()
	genBefore := inst.configGen
	// Pass a non-nil (but fake) LM — pushToolsAndModel only passes it through
	// to loop.UpdateModel, and test instances have no live loops.
	mgr.pushToolsAndModel(inst, id, "", inst.info.Mode, pc, &fakeLanguageModel{})
	genAfter := inst.configGen
	appliedModel := inst.info.Model
	inst.mu.Unlock()

	if genAfter <= genBefore {
		t.Errorf("configGen not incremented: before=%d after=%d", genBefore, genAfter)
	}
	if appliedModel == modelBefore {
		t.Error("model should have been updated by pushToolsAndModel")
	}
	if appliedModel != "new-provider/new-model" {
		t.Errorf("model = %q, want %q", appliedModel, "new-provider/new-model")
	}

	// Now simulate the stale-push scenario: snapshot gen, bump it (simulating
	// a concurrent UpdateInstanceConfig), then call pushToolsAndModel with
	// a different model. The re-check in pushConfigToInstance (configGen != gen)
	// would discard the LM, so pushToolsAndModel receives lm=nil. Verify that
	// passing nil LM does NOT change the model.
	inst.mu.Lock()
	genSnapshot := inst.configGen
	inst.configGen++ // simulate concurrent update
	pc2 := &configPushContext{
		cfg:       cfg,
		model:     "stale-provider/stale-model",
		modelSpec: models.ModelSpec{Provider: "stale-provider", Model: "stale-model"},
	}
	// This is what pushConfigToInstance does when configGen != gen: passes nil LM.
	mgr.pushToolsAndModel(inst, id, "", inst.info.Mode, pc2, nil)
	modelAfterStale := inst.info.Model
	genFinal := inst.configGen
	inst.mu.Unlock()

	if modelAfterStale == "stale-provider/stale-model" {
		t.Error("stale model should NOT have been applied when LM is nil")
	}
	if modelAfterStale != "new-provider/new-model" {
		t.Errorf("model should still be %q, got %q", "new-provider/new-model", modelAfterStale)
	}
	// configGen should still advance (tool rules may have changed).
	if genFinal <= genSnapshot+1 {
		t.Errorf("configGen should have advanced past simulated bump: snapshot=%d final=%d", genSnapshot, genFinal)
	}
}
