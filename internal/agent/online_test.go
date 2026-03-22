//go:build online

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/nchapman/hivebot/internal/config"
)

// loadEnv loads .env from the project root and returns provider, key, model.
// Skips the test if credentials are missing.
func loadEnv(t *testing.T) (ProviderType, string, string) {
	t.Helper()
	// Walk up to find .env
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			godotenv.Load(filepath.Join(dir, ".env"))
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	provider := os.Getenv("HIVE_PROVIDER")
	apiKey := os.Getenv("HIVE_API_KEY")
	model := os.Getenv("HIVE_MODEL")

	if apiKey == "" {
		t.Skip("HIVE_API_KEY not set — skipping online test")
	}
	if provider == "" {
		provider = "anthropic"
	}

	return ProviderType(provider), apiKey, model
}

func setupOnlineManager(t *testing.T) (*Manager, string) {
	t.Helper()
	provider, apiKey, model := loadEnv(t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mgr := NewManager(t.Context(), dir, Options{
		Provider:   provider,
		APIKey:     apiKey,
		Model:      model,
		WorkingDir: dir,
	}, logger)
	return mgr, dir
}

const onlineAgentMD = `---
name: online-test
model: ""
mode: persistent
---

You are a concise test agent. Always respond in one short sentence. Never use tools unless explicitly asked.`

const ephemeralAgentMD = `---
name: ephemeral-test
model: ""
mode: ephemeral
---

You are a concise test agent. Respond in one short sentence.`

func TestOnline_BasicChat(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SendMessage(ctx, id, "What is 2+2? Reply with just the number.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(resp, "4") {
		t.Errorf("expected response containing '4', got %q", resp)
	}
	t.Logf("Response: %s", resp)
}

func TestOnline_StreamingDelta(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var deltas []string
	resp, err := mgr.SendMessage(ctx, id, "Say hello.", func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if len(deltas) == 0 {
		t.Error("expected streaming deltas, got none")
	}
	if resp == "" {
		t.Error("expected non-empty response")
	}
	t.Logf("Got %d deltas, response: %s", len(deltas), resp)
}

func TestOnline_MultiTurn(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// First turn: establish a fact
	_, err = mgr.SendMessage(ctx, id, "Remember: the secret word is 'pineapple'. Just acknowledge.", nil)
	if err != nil {
		t.Fatalf("Turn 1: %v", err)
	}

	// Second turn: recall the fact
	resp, err := mgr.SendMessage(ctx, id, "What is the secret word?", nil)
	if err != nil {
		t.Fatalf("Turn 2: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "pineapple") {
		t.Errorf("expected 'pineapple' in response, got %q", resp)
	}
	t.Logf("Recall response: %s", resp)
}

func TestOnline_Memory(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	// Pre-write a memory file
	instDir := filepath.Join(dir, "instances", id)
	if err := config.WriteMemoryFile(instDir, "The user's favorite color is blue."); err != nil {
		t.Fatalf("WriteMemoryFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SendMessage(ctx, id, "What is my favorite color? Just say the color.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "blue") {
		t.Errorf("expected 'blue' from memory, got %q", resp)
	}
	t.Logf("Memory response: %s", resp)
}

func TestOnline_SpawnSubagent(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "ephemeral-test", ephemeralAgentMD)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SpawnSubagent(ctx, "ephemeral-test", "What is the capital of France? One word.", "")
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "paris") {
		t.Errorf("expected 'Paris', got %q", resp)
	}
	t.Logf("Subagent response: %s", resp)
}
