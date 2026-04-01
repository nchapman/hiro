package inference

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

func runMemoryTool(t *testing.T, tools []fantasy.AgentTool, name, input string) fantasy.ToolResponse {
	t.Helper()
	for _, tool := range tools {
		if tool.Info().Name == name {
			resp, err := tool.Run(context.Background(), fantasy.ToolCall{
				ID:    "test-call",
				Name:  name,
				Input: input,
			})
			if err != nil {
				t.Fatalf("unexpected error from %s: %v", name, err)
			}
			return resp
		}
	}
	t.Fatalf("tool %q not found", name)
	return fantasy.ToolResponse{}
}

func toolResponseText(resp fantasy.ToolResponse) string {
	return resp.Content
}

func TestAddMemory_Basic(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	resp := runMemoryTool(t, tools, "AddMemory", `{"content":"User prefers dark mode"}`)
	text := toolResponseText(resp)
	if !strings.Contains(text, "Memory saved") {
		t.Errorf("unexpected response: %s", text)
	}

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "User prefers dark mode") {
		t.Errorf("memory not found in file: %s", content)
	}
	if !strings.Contains(content, "- ") {
		t.Error("memory entry missing bullet prefix")
	}
}

func TestAddMemory_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteMemoryFile(dir, "- Existing memory [2026-01-01]\n"); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	runMemoryTool(t, tools, "AddMemory", `{"content":"New memory"}`)

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Existing memory") {
		t.Error("existing memory was lost")
	}
	if !strings.Contains(lines[1], "New memory") {
		t.Error("new memory not appended")
	}
}

func TestAddMemory_StripsNewlines(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	runMemoryTool(t, tools, "AddMemory", `{"content":"Line one\nLine two\r\nLine three"}`)

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %v", len(lines), lines)
	}
}

func TestAddMemory_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	resp := runMemoryTool(t, tools, "AddMemory", `{"content":"  "}`)
	if !resp.IsError {
		t.Error("expected error for empty content")
	}
}

func TestAddMemory_EvictsOldest(t *testing.T) {
	dir := t.TempDir()

	// Seed with maxMemoryEntries entries.
	var lines []string
	for i := range maxMemoryEntries {
		lines = append(lines, "- entry "+string(rune('A'+i%26))+" [2026-01-01]")
	}
	if err := config.WriteMemoryFile(dir, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	runMemoryTool(t, tools, "AddMemory", `{"content":"newest entry"}`)

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries := parseMemoryEntries(content)
	if len(entries) != maxMemoryEntries {
		t.Fatalf("expected %d entries, got %d", maxMemoryEntries, len(entries))
	}
	// First entry should have been evicted, last should be newest.
	if strings.Contains(entries[0], lines[0]) {
		t.Error("oldest entry should have been evicted")
	}
	if !strings.Contains(entries[len(entries)-1], "newest entry") {
		t.Error("newest entry should be last")
	}
}

func TestForgetMemory_Basic(t *testing.T) {
	dir := t.TempDir()
	seed := "- User prefers dark mode [2026-01-01]\n- Deploy requires VPN [2026-01-02]\n"
	if err := config.WriteMemoryFile(dir, seed); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"dark mode"}`)
	text := toolResponseText(resp)
	if !strings.Contains(text, "Forgot 1") {
		t.Errorf("unexpected response: %s", text)
	}

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "dark mode") {
		t.Error("memory should have been removed")
	}
	if !strings.Contains(content, "VPN") {
		t.Error("non-matching memory should remain")
	}
}

func TestForgetMemory_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteMemoryFile(dir, "- PostgreSQL is preferred [2026-01-01]\n"); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"postgresql"}`)
	if resp.IsError {
		t.Errorf("unexpected error: %s", toolResponseText(resp))
	}

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "PostgreSQL") {
		t.Error("case-insensitive match should have removed entry")
	}
}

func TestForgetMemory_NoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteMemoryFile(dir, "- Some memory [2026-01-01]\n"); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"nonexistent"}`)
	if !resp.IsError {
		t.Error("expected error when no memories match")
	}
}

func TestForgetMemory_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"anything"}`)
	if resp.IsError {
		t.Error("empty file should not be an error")
	}
	if !strings.Contains(toolResponseText(resp), "No memories") {
		t.Errorf("unexpected response: %s", toolResponseText(resp))
	}
}

func TestForgetMemory_AllRemoved(t *testing.T) {
	dir := t.TempDir()
	seed := "- VPN required [2026-01-01]\n- VPN setup guide [2026-01-02]\n"
	if err := config.WriteMemoryFile(dir, seed); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"VPN"}`)
	text := toolResponseText(resp)
	if !strings.Contains(text, "Forgot 2") {
		t.Errorf("expected 2 removals: %s", text)
	}

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(content) != "" {
		t.Errorf("expected empty file, got: %s", content)
	}
}

func TestForgetMemory_DoesNotMatchDateStamp(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteMemoryFile(dir, "- Important fact [2026-03-31]\n"); err != nil {
		t.Fatal(err)
	}

	tools := buildMemoryTools(dir)
	resp := runMemoryTool(t, tools, "ForgetMemory", `{"match":"2026"}`)
	if !resp.IsError {
		t.Error("matching against date stamp should not remove entries")
	}
}

func TestAddMemory_HasDateStamp(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	runMemoryTool(t, tools, "AddMemory", `{"content":"test memory"}`)

	content, err := config.ReadMemoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Should contain a date in YYYY-MM-DD format.
	if !strings.Contains(content, "[20") {
		t.Errorf("expected date stamp in memory: %s", content)
	}
}

func TestParseMemoryEntries_SkipsBlankLines(t *testing.T) {
	input := "- entry one\n\n- entry two\n\n\n"
	entries := parseMemoryEntries(input)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(entries), entries)
	}
}

func TestMemoryFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	tools := buildMemoryTools(dir)

	runMemoryTool(t, tools, "AddMemory", `{"content":"secret preference"}`)

	info, err := os.Stat(filepath.Join(dir, "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 permissions, got %04o", perm)
	}
}
