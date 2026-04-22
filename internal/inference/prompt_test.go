package inference

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

func TestBuildSystemPrompt_MinimalConfig(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "You are a helpful assistant."}
	got := buildSystemPrompt(cfg, EnvInfo{}, "")

	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Error("expected main prompt in output")
	}
	if !strings.Contains(got, "## Security") {
		t.Error("expected security section")
	}
	for _, section := range []string{"## Persona", "## Memories", "## Current Tasks", "## Secrets", "## Skills", "## Environment"} {
		if strings.Contains(got, section) {
			t.Errorf("unexpected section %q in minimal prompt", section)
		}
	}
}

func TestBuildSystemPrompt_IdentitySections(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "Main instructions."}
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/abc123",
		SessionDir:  "/hiro/instances/abc123/sessions/sess1",
		Mode:        config.ModePersistent,
	}
	got := buildSystemPrompt(cfg, env, "Friendly and precise.")

	for _, want := range []string{
		"## Environment", "workspace/", "memory.md", "persona.md",
		"## Persona", "Friendly and precise.",
		"Main instructions.",
		"## Security",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
	// Dynamic content should NOT be in system prompt.
	for _, absent := range []string{"## Memories", "## Current Tasks", "## Secrets", "## Skills"} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected dynamic section %q in system prompt", absent)
		}
	}
}

func TestBuildSystemPrompt_SectionOrder(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "MAIN_INSTRUCTIONS"}
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/x",
		SessionDir:  "/hiro/instances/x/sessions/y",
		Mode:        config.ModePersistent,
	}
	got := buildSystemPrompt(cfg, env, "PERSONA")

	order := []string{
		"## Environment",
		"MAIN_INSTRUCTIONS",
		"PERSONA",
		"## Security",
	}
	lastIdx := -1
	for _, s := range order {
		idx := strings.Index(got, s)
		if idx < 0 {
			t.Fatalf("missing %q in prompt", s)
		}
		if idx <= lastIdx {
			t.Errorf("%q appeared before expected position", s)
		}
		lastIdx = idx
	}
}

// --- Delta replay tests ---

func TestReplayAnnounced_Empty(t *testing.T) {
	got := replayAnnounced("agents", nil)
	if len(got) != 0 {
		t.Fatalf("expected empty set, got %d entries", len(got))
	}
}

func TestReplayAnnounced_SingleDelta(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("text", "agents", []string{"assistant", "critic"}, nil),
	}
	got := replayAnnounced("agents", history)
	if !got["assistant"] || !got["critic"] {
		t.Errorf("expected {assistant, critic}, got %v", got)
	}
}

func TestReplayAnnounced_AddThenRemove(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("initial", "agents", []string{"a", "b", "c"}, nil),
		buildDeltaMessage("remove b", "agents", nil, []string{"b"}),
	}
	got := replayAnnounced("agents", history)
	if !got["a"] || !got["c"] || got["b"] {
		t.Errorf("expected {a, c}, got %v", got)
	}
}

func TestReplayAnnounced_RemoveThenReAdd(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("initial", "agents", []string{"a", "b"}, nil),
		buildDeltaMessage("remove b", "agents", nil, []string{"b"}),
		buildDeltaMessage("add b back", "agents", []string{"b"}, nil),
	}
	got := replayAnnounced("agents", history)
	if !got["a"] || !got["b"] {
		t.Errorf("expected {a, b}, got %v", got)
	}
}

func TestReplayAnnounced_FiltersByType(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("agents", "agents", []string{"assistant"}, nil),
		buildDeltaMessage("nodes", "nodes", []string{"node1"}, nil),
	}
	agents := replayAnnounced("agents", history)
	if !agents["assistant"] || agents["node1"] {
		t.Errorf("expected only assistant, got %v", agents)
	}
	nodes := replayAnnounced("nodes", history)
	if !nodes["node1"] || nodes["assistant"] {
		t.Errorf("expected only node1, got %v", nodes)
	}
}

func TestReplayAnnounced_SkipsNonDeltaMessages(t *testing.T) {
	history := []fantasy.Message{
		fantasy.NewUserMessage("hello"),
		buildDeltaMessage("agents", "agents", []string{"assistant"}, nil),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "hi there"}}},
	}
	got := replayAnnounced("agents", history)
	if len(got) != 1 || !got["assistant"] {
		t.Errorf("expected {assistant}, got %v", got)
	}
}

func TestComputeDeltas_NilProviders(t *testing.T) {
	got := computeDeltas(nil, nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected no deltas, got %d", len(got))
	}
}

// findEntry extracts a DeltaEntry for the given context type from a message.
func findEntry(t *testing.T, msg fantasy.Message, contextType string) DeltaEntry {
	t.Helper()
	dr := extractDeltaReplay(msg)
	if dr == nil {
		t.Fatalf("no DeltaReplay in message")
	}
	for _, e := range dr.Entries {
		if e.ContextType == contextType {
			return e
		}
	}
	t.Fatalf("no entry for context type %q", contextType)
	return DeltaEntry{}
}

func TestComputeDeltas_Dedup(t *testing.T) {
	p1 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("first", "agents", []string{"a"}, nil)}
	}
	p2 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("second", "agents", []string{"b"}, nil)}
	}
	got := computeDeltas([]ContextProvider{p1, p2}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(got))
	}
	// First provider should win.
	e := findEntry(t, got[0], "agents")
	if len(e.AddedNames) == 0 || e.AddedNames[0] != "a" {
		t.Error("expected first provider's entry to win dedup")
	}
}

func TestComputeDeltas_MergesIntoSingleMessage(t *testing.T) {
	p1 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("agents here", "agents", []string{"a"}, nil)}
	}
	p2 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("skills here", "skills", []string{"s"}, nil)}
	}
	got := computeDeltas([]ContextProvider{p1, p2}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(got))
	}
	// Both entries should be in the single message.
	dr := extractDeltaReplay(got[0])
	if len(dr.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(dr.Entries))
	}
	// Text should contain both.
	text := textPartText(t, got[0].Content[0])
	if !strings.Contains(text, "agents here") || !strings.Contains(text, "skills here") {
		t.Error("merged message should contain text from both providers")
	}
}

func TestDeltaReplay_JSONRoundTrip(t *testing.T) {
	msg := buildDeltaMessage("test", "agents", []string{"assistant"}, []string{"old"})

	// Marshal to JSON (simulates DB storage).
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Unmarshal back (simulates DB retrieval).
	var restored fantasy.Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Extract DeltaReplay from restored message.
	e := findEntry(t, restored, "agents")
	if len(e.AddedNames) != 1 || e.AddedNames[0] != "assistant" {
		t.Errorf("expected added [assistant], got %v", e.AddedNames)
	}
	if len(e.RemovedNames) != 1 || e.RemovedNames[0] != "old" {
		t.Errorf("expected removed [old], got %v", e.RemovedNames)
	}
}

// --- Tool wrapper tests ---

func TestFantasyTools(t *testing.T) {
	a := fantasy.NewAgentTool("A", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	b := fantasy.NewAgentTool("B", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tools := []Tool{wrap(a), wrap(b)}
	ft := fantasyTools(tools)
	if len(ft) != 2 {
		t.Fatalf("expected 2 fantasy tools, got %d", len(ft))
	}
	if ft[0].Info().Name != "A" || ft[1].Info().Name != "B" {
		t.Error("fantasy tools should preserve order and identity")
	}
}

func TestWrapAndWrapAll(t *testing.T) {
	a := fantasy.NewAgentTool("A", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tool := wrap(a)
	if tool.Info().Name != "A" {
		t.Error("wrap should preserve tool identity")
	}

	b := fantasy.NewAgentTool("B", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tools := wrapAll([]fantasy.AgentTool{a, b})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

// --- Environment section tests ---

func TestBuildEnvironmentSection_Persistent(t *testing.T) {
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/abc-123",
		SessionDir:  "/hiro/instances/abc-123/sessions/sess-456",
		Mode:        config.ModePersistent,
	}
	got := buildEnvironmentSection(env)

	for _, want := range []string{
		"workspace/", "agents/", "memory.md", "persona.md",
		"todos.yaml", "scratch/", "tmp/",
		"/hiro/instances/abc-123",
		"/hiro/instances/abc-123/sessions/sess-456",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in environment section", want)
		}
	}
}

func TestComputeDeltas_ProviderReturnsNil(t *testing.T) {
	p := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult { return nil }
	got := computeDeltas([]ContextProvider{p}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected no deltas when provider returns nil, got %d", len(got))
	}
}

func TestExtractDeltaReplay_NoProviderOptions(t *testing.T) {
	msg := fantasy.NewUserMessage("plain message")
	if dr := extractDeltaReplay(msg); dr != nil {
		t.Error("expected nil for message without ProviderOptions")
	}
}

func TestExtractDeltaReplay_WrongKey(t *testing.T) {
	msg := fantasy.NewUserMessage("test")
	// ProviderOptions with a different key.
	msg.ProviderOptions = fantasy.ProviderOptions{}
	if dr := extractDeltaReplay(msg); dr != nil {
		t.Error("expected nil for message with no delta key")
	}
}

func TestBuildDeltaMessage_Structure(t *testing.T) {
	msg := buildDeltaMessage("hello world", "agents", []string{"b", "a"}, []string{"d", "c"})

	// Content should be wrapped in system-reminder.
	text := textPartText(t, msg.Content[0])
	if !strings.Contains(text, "<system-reminder>") || !strings.Contains(text, "hello world") {
		t.Error("expected system-reminder wrapped content")
	}

	// ProviderOptions should contain sorted replay data.
	e := findEntry(t, msg, "agents")
	// Verify sorted.
	if e.AddedNames[0] != "a" || e.AddedNames[1] != "b" {
		t.Errorf("expected sorted added [a, b], got %v", e.AddedNames)
	}
	if e.RemovedNames[0] != "c" || e.RemovedNames[1] != "d" {
		t.Errorf("expected sorted removed [c, d], got %v", e.RemovedNames)
	}
}

func TestReplayAnnounced_DuplicateAdd(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("first", "agents", []string{"a"}, nil),
		buildDeltaMessage("again", "agents", []string{"a"}, nil),
	}
	got := replayAnnounced("agents", history)
	if len(got) != 1 || !got["a"] {
		t.Errorf("duplicate add should be idempotent, got %v", got)
	}
}

func TestBuildEnvironmentSection_Ephemeral(t *testing.T) {
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/eph-1",
		SessionDir:  "/hiro/instances/eph-1/sessions/s1",
		Mode:        config.ModeEphemeral,
	}
	got := buildEnvironmentSection(env)

	if strings.Contains(got, "memory.md") {
		t.Error("ephemeral agents should not see memory.md")
	}
	if !strings.Contains(got, "scratch/") {
		t.Error("expected scratch/ in ephemeral env")
	}
	if strings.Contains(got, "Your instance directory") {
		t.Error("ephemeral agents should not get instance directory callout")
	}
	if !strings.Contains(got, "Your session directory") {
		t.Error("ephemeral agents should get session directory callout")
	}
}

// --- Content hash helper tests ---

func TestContentHash_Deterministic(t *testing.T) {
	h1 := contentHash("hello world")
	h2 := contentHash("hello world")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if len(h1) != 16 {
		t.Errorf("expected 16 hex chars, got %d", len(h1))
	}
}

func TestContentHash_DifferentInputs(t *testing.T) {
	h1 := contentHash("hello")
	h2 := contentHash("world")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestReplayLatestHash(t *testing.T) {
	history := []fantasy.Message{
		buildContentMessage("first", "memory", "aaa"),
		buildContentMessage("second", "memory", "bbb"),
		buildContentMessage("other", "todos", "ccc"),
	}
	if got := replayLatestHash("memory", history); got != "bbb" {
		t.Errorf("expected latest hash 'bbb', got %q", got)
	}
	if got := replayLatestHash("todos", history); got != "ccc" {
		t.Errorf("expected 'ccc', got %q", got)
	}
	if got := replayLatestHash("unknown", history); got != "" {
		t.Errorf("expected empty for unknown type, got %q", got)
	}
}

func TestBuildContentMessage_Structure(t *testing.T) {
	msg := buildContentMessage("test content", "memory", "abc123")
	text := textPartText(t, msg.Content[0])
	if !strings.Contains(text, "<system-reminder>") || !strings.Contains(text, "test content") {
		t.Error("expected system-reminder wrapped content")
	}
	e := findEntry(t, msg, "memory")
	if e.ContentHash != "abc123" {
		t.Errorf("expected content hash 'abc123', got %q", e.ContentHash)
	}
	if len(e.AddedNames) != 0 || len(e.RemovedNames) != 0 {
		t.Error("content hash message should not have added/removed names")
	}
}

// --- Skill provider tests ---

func writeSkillFile(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillProvider_Initial(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "deploy", "Deploy to prod.")

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	dr := p(active, nil)
	if dr == nil {
		t.Fatal("expected initial announcement")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Skills") || !strings.Contains(text, "**deploy**") {
		t.Errorf("expected skills listing, got: %s", text)
	}
}

func TestSkillProvider_NoSkillTool(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "deploy", "Deploy.")

	p := SkillProvider(dir, "")
	dr := p(map[string]bool{"Bash": true}, nil)
	if dr != nil {
		t.Error("should return nil when Skill tool is not active")
	}
}

func TestSkillProvider_NoChange(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "deploy", "Deploy.")

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	history := []fantasy.Message{
		buildDeltaMessage("prior", "skills", []string{"deploy"}, nil),
	}
	dr := p(active, history)
	if dr != nil {
		t.Error("should return nil when skills unchanged")
	}
}

func TestSkillProvider_AllRemoved(t *testing.T) {
	dir := t.TempDir()
	// No skills directory → empty skills

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	history := []fantasy.Message{
		buildDeltaMessage("prior", "skills", []string{"deploy"}, nil),
	}
	dr := p(active, history)
	if dr == nil {
		t.Fatal("expected removal delta when all skills removed")
	}
	entry := findEntry(t, dr.Message, "skills")
	if len(entry.RemovedNames) != 1 || entry.RemovedNames[0] != "deploy" {
		t.Errorf("expected removed [deploy], got %v", entry.RemovedNames)
	}
}

func TestSkillProvider_NoSkills(t *testing.T) {
	dir := t.TempDir()
	// No skills directory, no history
	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	dr := p(active, nil)
	if dr != nil {
		t.Error("should return nil when no skills and no history")
	}
}

func TestSkillProvider_CompactionResets(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "deploy", "Deploy.")

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	// After compaction, history is empty → full re-announcement.
	dr := p(active, nil)
	if dr == nil {
		t.Fatal("expected full re-announcement after compaction")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Skills") {
		t.Error("expected initial-style listing after compaction")
	}
}

func TestSkillProvider_SimultaneousAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "review", "Review code.")

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	history := []fantasy.Message{
		buildDeltaMessage("prior", "skills", []string{"deploy"}, nil),
	}
	dr := p(active, history)
	if dr == nil {
		t.Fatal("expected delta")
	}
	entry := findEntry(t, dr.Message, "skills")
	if len(entry.AddedNames) != 1 || entry.AddedNames[0] != "review" {
		t.Errorf("expected added [review], got %v", entry.AddedNames)
	}
	if len(entry.RemovedNames) != 1 || entry.RemovedNames[0] != "deploy" {
		t.Errorf("expected removed [deploy], got %v", entry.RemovedNames)
	}
}

func TestSkillProvider_Delta(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	writeSkillFile(t, skillsDir, "deploy", "Deploy.")
	writeSkillFile(t, skillsDir, "review", "Review code.")

	p := SkillProvider(dir, "")
	active := map[string]bool{"Skill": true}
	history := []fantasy.Message{
		buildDeltaMessage("prior", "skills", []string{"deploy"}, nil),
	}
	dr := p(active, history)
	if dr == nil {
		t.Fatal("expected delta for new skill")
	}
	entry := findEntry(t, dr.Message, "skills")
	if len(entry.AddedNames) != 1 || entry.AddedNames[0] != "review" {
		t.Errorf("expected added [review], got %v", entry.AddedNames)
	}
}

// --- Secret provider tests ---

func TestSecretProvider_Initial(t *testing.T) {
	fn := func() []string { return []string{"API_KEY", "DB_PASS"} }
	p := SecretProvider(fn)
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected initial announcement")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Secrets") || !strings.Contains(text, "`API_KEY`") {
		t.Errorf("expected secrets listing, got: %s", text)
	}
}

func TestSecretProvider_NoChange(t *testing.T) {
	fn := func() []string { return []string{"API_KEY"} }
	p := SecretProvider(fn)
	history := []fantasy.Message{
		buildDeltaMessage("prior", "secrets", []string{"API_KEY"}, nil),
	}
	dr := p(nil, history)
	if dr != nil {
		t.Error("should return nil when secrets unchanged")
	}
}

func TestSecretProvider_Delta(t *testing.T) {
	fn := func() []string { return []string{"API_KEY", "NEW_SECRET"} }
	p := SecretProvider(fn)
	history := []fantasy.Message{
		buildDeltaMessage("prior", "secrets", []string{"API_KEY"}, nil),
	}
	dr := p(nil, history)
	if dr == nil {
		t.Fatal("expected delta for new secret")
	}
	entry := findEntry(t, dr.Message, "secrets")
	if len(entry.AddedNames) != 1 || entry.AddedNames[0] != "NEW_SECRET" {
		t.Errorf("expected added [NEW_SECRET], got %v", entry.AddedNames)
	}
}

func TestSecretProvider_NilFn(t *testing.T) {
	p := SecretProvider(nil)
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil with nil function")
	}
}

func TestSecretProvider_EmptySlice(t *testing.T) {
	fn := func() []string { return []string{} }
	p := SecretProvider(fn)
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil with empty secrets and no history")
	}
}

func TestSecretProvider_CompactionResets(t *testing.T) {
	fn := func() []string { return []string{"API_KEY"} }
	p := SecretProvider(fn)
	// After compaction, history is empty → full re-announcement.
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected full re-announcement after compaction")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Secrets") {
		t.Error("expected initial-style listing after compaction")
	}
}

func TestSecretProvider_SimultaneousAddAndRemove(t *testing.T) {
	fn := func() []string { return []string{"NEW_KEY"} }
	p := SecretProvider(fn)
	history := []fantasy.Message{
		buildDeltaMessage("prior", "secrets", []string{"OLD_KEY"}, nil),
	}
	dr := p(nil, history)
	if dr == nil {
		t.Fatal("expected delta")
	}
	entry := findEntry(t, dr.Message, "secrets")
	if len(entry.AddedNames) != 1 || entry.AddedNames[0] != "NEW_KEY" {
		t.Errorf("expected added [NEW_KEY], got %v", entry.AddedNames)
	}
	if len(entry.RemovedNames) != 1 || entry.RemovedNames[0] != "OLD_KEY" {
		t.Errorf("expected removed [OLD_KEY], got %v", entry.RemovedNames)
	}
}

func TestSecretProvider_AllRemoved(t *testing.T) {
	fn := func() []string { return []string{} }
	p := SecretProvider(fn)
	history := []fantasy.Message{
		buildDeltaMessage("prior", "secrets", []string{"API_KEY"}, nil),
	}
	dr := p(nil, history)
	if dr == nil {
		t.Fatal("expected removal delta when all secrets removed")
	}
	entry := findEntry(t, dr.Message, "secrets")
	if len(entry.RemovedNames) != 1 || entry.RemovedNames[0] != "API_KEY" {
		t.Errorf("expected removed [API_KEY], got %v", entry.RemovedNames)
	}
}

// --- Memory provider tests ---

func TestMemoryProvider_Initial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("Remember: user likes Go."), 0o644); err != nil {
		t.Fatal(err)
	}
	p := MemoryProvider(dir)
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected initial memory announcement")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Memories") || !strings.Contains(text, "user likes Go") {
		t.Errorf("expected memory content, got: %s", text)
	}
}

func TestMemoryProvider_NoChange(t *testing.T) {
	dir := t.TempDir()
	content := "Remember: user likes Go."
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := contentHash(content)
	p := MemoryProvider(dir)
	history := []fantasy.Message{
		buildContentMessage("prior", "memory", hash),
	}
	dr := p(nil, history)
	if dr != nil {
		t.Error("should return nil when memory unchanged")
	}
}

func TestMemoryProvider_Changed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := MemoryProvider(dir)
	history := []fantasy.Message{
		buildContentMessage("prior", "memory", "old-hash"),
	}
	dr := p(nil, history)
	if dr == nil {
		t.Fatal("expected re-emission when memory changed")
	}
	entry := findEntry(t, dr.Message, "memory")
	if entry.ContentHash == "old-hash" {
		t.Error("expected new content hash")
	}
}

func TestMemoryProvider_CompactionResets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("Remember: user likes Go."), 0o644); err != nil {
		t.Fatal(err)
	}
	p := MemoryProvider(dir)
	// After compaction, history is empty → re-emit even if content unchanged.
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected re-emission after compaction")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Memories") {
		t.Error("expected memories section")
	}
}

func TestMemoryProvider_EmptyDir(t *testing.T) {
	p := MemoryProvider("")
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil for empty instanceDir")
	}
}

func TestMemoryProvider_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	// No memory.md file → ReadMemoryFile returns ""
	p := MemoryProvider(dir)
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil for missing memory file")
	}
}

// --- Todo provider tests ---

func TestTodoProvider_Initial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "todos.yaml"), []byte("- content: Fix bug\n  status: pending\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := TodoProvider(dir)
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected initial todo announcement")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Current Tasks") || !strings.Contains(text, "Fix bug") {
		t.Errorf("expected todo content, got: %s", text)
	}
}

func TestTodoProvider_NoChange(t *testing.T) {
	dir := t.TempDir()
	yamlContent := "- content: Fix bug\n  status: pending\n"
	if err := os.WriteFile(filepath.Join(dir, "todos.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-compute the hash of the formatted output.
	formatted := config.FormatTodos([]config.Todo{{Content: "Fix bug", Status: config.TodoStatusPending}})
	hash := contentHash(formatted)
	p := TodoProvider(dir)
	history := []fantasy.Message{
		buildContentMessage("prior", "todos", hash),
	}
	dr := p(nil, history)
	if dr != nil {
		t.Error("should return nil when todos unchanged")
	}
}

func TestTodoProvider_Changed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "todos.yaml"), []byte("- content: New task\n  status: in_progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := TodoProvider(dir)
	history := []fantasy.Message{
		buildContentMessage("prior", "todos", "old-hash"),
	}
	dr := p(nil, history)
	if dr == nil {
		t.Fatal("expected re-emission when todos changed")
	}
}

func TestTodoProvider_CompactionResets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "todos.yaml"), []byte("- content: Fix bug\n  status: pending\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := TodoProvider(dir)
	// After compaction, history is empty → re-emit.
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected re-emission after compaction")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Current Tasks") {
		t.Error("expected tasks section")
	}
}

func TestTodoProvider_EmptyDir(t *testing.T) {
	p := TodoProvider("")
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil for empty sessionDir")
	}
}

func TestTodoProvider_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	// No todos.yaml → ReadTodos returns nil
	p := TodoProvider(dir)
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil for missing todos file")
	}
}

// --- Mount provider tests ---

func TestMountProvider_NilWhenNoMountsDir(t *testing.T) {
	// Empty root (no mounts subdir) → nil.
	p := MountProvider(t.TempDir())
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil when no mounts dir")
	}
}

func TestMountProvider_EmptyRoot(t *testing.T) {
	p := MountProvider("")
	if dr := p(nil, nil); dr != nil {
		t.Error("should return nil for empty rootDir")
	}
}

func TestMountProvider_EmitsOnFirstDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mounts", "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := MountProvider(root)
	dr := p(nil, nil)
	if dr == nil {
		t.Fatal("expected first-discovery emission")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "## Mounts") || !strings.Contains(text, "photos") {
		t.Errorf("expected mount listing, got: %s", text)
	}
}

func TestMountProvider_SuppressesOnUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mounts", "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := MountProvider(root)
	first := p(nil, nil)
	if first == nil {
		t.Fatal("expected first emission")
	}
	history := []fantasy.Message{first.Message}
	if second := p(nil, history); second != nil {
		t.Error("expected nil on unchanged mount set")
	}
}

func TestMountProvider_ReEmitsOnModeChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode checks")
	}
	root := t.TempDir()
	mountPath := filepath.Join(root, "mounts", "photos")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mountPath, 0o755) })

	p := MountProvider(root)
	first := p(nil, nil)
	if first == nil {
		t.Fatal("expected first emission")
	}
	firstText := textPartText(t, first.Message.Content[0])
	if !strings.Contains(firstText, "(rw)") {
		t.Fatalf("expected rw initially, got: %s", firstText)
	}

	// Flip to read-only — mode probe (unix.Access W_OK) should now return EACCES.
	if err := os.Chmod(mountPath, 0o555); err != nil {
		t.Fatal(err)
	}

	history := []fantasy.Message{first.Message}
	second := p(nil, history)
	if second == nil {
		t.Fatal("expected re-emission after mode flip")
	}
	secondText := textPartText(t, second.Message.Content[0])
	if !strings.Contains(secondText, "(ro)") {
		t.Errorf("expected ro after chmod, got: %s", secondText)
	}
}
