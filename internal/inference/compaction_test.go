package inference

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	platformdb "github.com/nchapman/hivebot/internal/platform/db"
)

// fakeSummarizer returns canned summaries for testing.
type fakeSummarizer struct {
	calls   int
	results []string
}

func (f *fakeSummarizer) Summarize(_ context.Context, _, _ string) (string, error) {
	idx := f.calls
	f.calls++
	if idx < len(f.results) {
		return f.results[idx], nil
	}
	return "summary of conversation", nil
}

func TestCompactIfNeeded_NoCompactionBelowThreshold(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Add a few small messages — well below any compaction threshold.
	for i := 0; i < 5; i++ {
		appendMsg(t, pdb, "s1", "user", "small msg", 100)
	}

	summarizer := &fakeSummarizer{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, DefaultCompactionConfig(), logger)

	if err := compactor.CompactIfNeeded(context.Background()); err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if summarizer.calls > 0 {
		t.Error("summarizer should not be called below threshold")
	}
}

func TestCompactIfNeeded_LeafPassTriggered(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		TokenBudget:      180_000,
		FreshTailCount:   5,
		LeafChunkTokens:  500,  // low threshold to trigger easily
		LeafTargetTokens: 200,
		LeafMinFanout:    3,
		CompactThreshold: 0.75,
	}

	// Add enough messages outside the fresh tail to trigger leaf pass.
	for i := 0; i < 20; i++ {
		appendMsg(t, pdb, "s1", "user", "message for compaction testing", 100)
	}

	summarizer := &fakeSummarizer{results: []string{"leaf summary of messages"}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	if err := compactor.CompactIfNeeded(context.Background()); err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if summarizer.calls == 0 {
		t.Error("expected summarizer to be called for leaf pass")
	}

	// Verify a summary was created in context items.
	items, err := pdb.GetContextItems("s1")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	hasSummary := false
	for _, item := range items {
		if item.ItemType == "summary" {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Error("expected at least one summary context item after compaction")
	}
	// Verify that messages were actually consumed — fewer message items should remain.
	msgCount := 0
	for _, item := range items {
		if item.ItemType == "message" {
			msgCount++
		}
	}
	if msgCount >= 20 {
		t.Error("expected some messages to be replaced by summary, none were removed")
	}
}

func TestSummarizationPrompt_Variants(t *testing.T) {
	tests := []struct {
		depth      int
		aggressive bool
		contains   string
	}{
		{0, false, "Summarize the following conversation segment"},
		{0, true, "extremely concise"},
		{1, false, "Condense the following summaries"},
		{2, false, "Distill the following summaries"},
	}
	for _, tt := range tests {
		got := summarizationPrompt(tt.depth, tt.aggressive)
		if !strings.Contains(got, tt.contains) {
			t.Errorf("depth=%d aggressive=%v: missing %q", tt.depth, tt.aggressive, tt.contains)
		}
	}
}

func TestBuildLeafInput(t *testing.T) {
	got := buildLeafInput(nil)
	if got != "" {
		t.Errorf("expected empty for nil, got %q", got)
	}

	msgs := []platformdb.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	got = buildLeafInput(msgs)
	if !strings.HasPrefix(got, "[") {
		t.Error("expected timestamp prefix")
	}
	if !strings.Contains(got, "user: hello") {
		t.Error("expected role and content in output")
	}
	if !strings.Contains(got, "assistant: hi there") {
		t.Error("expected second message in output")
	}
}

func TestFallbackTruncate(t *testing.T) {
	short := "hello"
	if got := fallbackTruncate(short, 100); got != short {
		t.Errorf("short string should not be truncated")
	}

	long := strings.Repeat("x", 1000)
	got := fallbackTruncate(long, 10) // 10 tokens = 40 chars
	if len(got) > 80 {
		t.Errorf("truncated result too long: %d chars", len(got))
	}
	if !strings.Contains(got, "[Truncated") {
		t.Error("expected truncation marker")
	}
}

func TestSummarizeWithEscalation_FallbackTruncation(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Return a summary that's always too long, forcing fallback truncation.
	longSummary := strings.Repeat("word ", 5000) // ~25000 chars ≈ 6250 tokens
	summarizer := &fakeSummarizer{results: []string{longSummary, longSummary}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	compactor := NewCompactor(pdb, "s1", summarizer, DefaultCompactionConfig(), logger)
	got, err := compactor.summarizeWithEscalation(context.Background(), 0, "input text", 10000)
	if err != nil {
		t.Fatalf("summarizeWithEscalation: %v", err)
	}

	// Should have been truncated to fit LeafTargetTokens (1200 tokens = 4800 chars).
	if EstimateTokens(got) > DefaultCompactionConfig().LeafTargetTokens+10 {
		t.Errorf("result tokens %d exceeds target %d", EstimateTokens(got), DefaultCompactionConfig().LeafTargetTokens)
	}
	if summarizer.calls != 2 {
		t.Errorf("expected 2 summarizer calls (normal + aggressive), got %d", summarizer.calls)
	}
}

func TestGenerateSummaryID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateSummaryID()
		if !strings.HasPrefix(id, "sum_") {
			t.Fatalf("id %q missing sum_ prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d generations", id, i)
		}
		seen[id] = true
	}
}
