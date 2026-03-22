package history

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeSummarizer returns a short canned summary for testing.
type fakeSummarizer struct {
	calls    int
	response string // if set, always return this
}

func (f *fakeSummarizer) Summarize(_ context.Context, _, input string) (string, error) {
	f.calls++
	if f.response != "" {
		return f.response, nil
	}
	// Default: return a short summary
	return fmt.Sprintf("Summary of %d chars", len(input)), nil
}

// bloatySummarizer returns a summary larger than the input (triggers escalation).
type bloatySummarizer struct {
	calls int
}

func (b *bloatySummarizer) Summarize(_ context.Context, systemPrompt, input string) (string, error) {
	b.calls++
	if strings.Contains(systemPrompt, "extremely concise") {
		// Aggressive mode — return something reasonable
		return "Brief summary.", nil
	}
	// Normal mode — return something bloated
	return strings.Repeat("x", len(input)*2), nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func testConfig() Config {
	return Config{
		TokenBudget:          1000,
		FreshTailCount:       2,
		LeafChunkTokens:      200, // low thresholds for testing
		LeafTargetTokens:     50,
		CondenseTargetTokens: 80,
		CompactThreshold:     0.75,
		LeafMinFanout:        3,
		CondenseMinFanout:    2,
	}
}

func TestLeafPass(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	c := NewCompactor(s, fs, testConfig(), testLogger())
	ctx := context.Background()

	// Add enough messages to trigger leaf pass
	// Config: LeafChunkTokens=100, FreshTailCount=2, LeafMinFanout=3
	for i := 0; i < 6; i++ {
		s.AppendMessage("user", strings.Repeat("word ", 10), `{}`, 50)
	}

	// Verify tokens outside tail
	tokens, _ := s.MessageTokensOutsideTail(2)
	if tokens < 100 {
		t.Fatalf("expected >= 100 tokens outside tail, got %d", tokens)
	}

	if err := c.leafPass(ctx); err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	if fs.calls != 1 {
		t.Errorf("expected 1 summarizer call, got %d", fs.calls)
	}

	// Context items should now have fewer entries
	items, _ := s.GetContextItems()
	// Should have: 1 summary + 2 tail messages (or more if not all fit in chunk)
	hasSummary := false
	for _, it := range items {
		if it.ItemType == "summary" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Error("expected at least one summary in context items")
	}
}

func TestLeafPass_MinFanout(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	cfg := testConfig()
	cfg.LeafMinFanout = 10 // higher than we'll add
	c := NewCompactor(s, fs, cfg, testLogger())
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		s.AppendMessage("user", "message", `{}`, 50)
	}

	if err := c.leafPass(ctx); err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	// Should not have called summarizer (not enough messages)
	if fs.calls != 0 {
		t.Errorf("expected 0 summarizer calls, got %d", fs.calls)
	}
}

func TestCondensationPass(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	cfg := testConfig()
	cfg.CondenseMinFanout = 2
	c := NewCompactor(s, fs, cfg, testLogger())
	ctx := context.Background()

	// Manually create 3 leaf summaries in context_items
	now := time.Now()
	for i := 0; i < 3; i++ {
		// Add a message then replace with summary
		s.AppendMessage("user", "msg", `{}`, 10)
		items, _ := s.GetContextItems()
		lastItem := items[len(items)-1]

		sumID := fmt.Sprintf("sum_cond_%d", i)
		s.CreateSummary(Summary{
			ID: sumID, Kind: "leaf", Depth: 0,
			Content:      fmt.Sprintf("Leaf summary %d", i),
			Tokens:       20,
			EarliestAt:   now,
			LatestAt:     now,
			SourceTokens: 10,
		})
		s.ReplaceContextItems(lastItem.Ordinal, lastItem.Ordinal, sumID)
	}

	condensed, err := c.condensationPass(ctx)
	if err != nil {
		t.Fatalf("condensationPass: %v", err)
	}
	if !condensed {
		t.Error("expected condensation to occur")
	}
	if fs.calls != 1 {
		t.Errorf("expected 1 summarizer call, got %d", fs.calls)
	}

	// Should now have a depth-1 summary in context
	maxD, _ := s.MaxSummaryDepth()
	if maxD != 1 {
		t.Errorf("expected max depth 1, got %d", maxD)
	}
}

func TestEscalation_NormalToAggressive(t *testing.T) {
	s := tempStore(t)
	bs := &bloatySummarizer{}
	c := NewCompactor(s, bs, testConfig(), testLogger())
	ctx := context.Background()

	// Add enough messages
	for i := 0; i < 6; i++ {
		s.AppendMessage("user", strings.Repeat("word ", 10), `{}`, 50)
	}

	if err := c.leafPass(ctx); err != nil {
		t.Fatalf("leafPass with escalation: %v", err)
	}

	// Should have called summarizer twice (normal + aggressive)
	if bs.calls != 2 {
		t.Errorf("expected 2 summarizer calls (escalation), got %d", bs.calls)
	}
}

// alwaysBloatySummarizer always returns bloated output, triggering fallback truncation.
type alwaysBloatySummarizer struct {
	calls int
}

func (a *alwaysBloatySummarizer) Summarize(_ context.Context, _, input string) (string, error) {
	a.calls++
	return strings.Repeat("x", len(input)*2), nil
}

func TestEscalation_FallbackTruncation(t *testing.T) {
	s := tempStore(t)
	abs := &alwaysBloatySummarizer{}
	c := NewCompactor(s, abs, testConfig(), testLogger())
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		s.AppendMessage("user", strings.Repeat("word ", 10), `{}`, 50)
	}

	if err := c.leafPass(ctx); err != nil {
		t.Fatalf("leafPass with fallback: %v", err)
	}

	// Should have called summarizer twice (normal + aggressive), then used fallback
	if abs.calls != 2 {
		t.Errorf("expected 2 summarizer calls before fallback, got %d", abs.calls)
	}

	// A summary should still have been created (via truncation)
	items, _ := s.GetContextItems()
	hasSummary := false
	for _, it := range items {
		if it.ItemType == "summary" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Error("expected summary from fallback truncation")
	}
}

func TestCompactIfNeeded_NoAction(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	c := NewCompactor(s, fs, testConfig(), testLogger())
	ctx := context.Background()

	// Only 2 messages — below all thresholds
	s.AppendMessage("user", "hello", `{}`, 5)
	s.AppendMessage("assistant", "hi", `{}`, 5)

	if err := c.CompactIfNeeded(ctx); err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if fs.calls != 0 {
		t.Errorf("expected no summarizer calls, got %d", fs.calls)
	}
}

func TestCompactIfNeeded_TriggersLeaf(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	c := NewCompactor(s, fs, testConfig(), testLogger())
	ctx := context.Background()

	// Add enough to trigger leaf (100+ tokens outside tail of 2)
	for i := 0; i < 8; i++ {
		s.AppendMessage("user", strings.Repeat("word ", 10), `{}`, 50)
	}

	if err := c.CompactIfNeeded(ctx); err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if fs.calls == 0 {
		t.Error("expected at least one summarizer call")
	}
}

func TestFullSweep(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{response: "Short summary."}
	cfg := testConfig()
	cfg.TokenBudget = 200 // very small budget to force multiple passes
	cfg.LeafChunkTokens = 100
	cfg.LeafMinFanout = 2
	cfg.CondenseMinFanout = 2
	c := NewCompactor(s, fs, cfg, testLogger())
	ctx := context.Background()

	// Add many messages to force multiple passes (each 20 tokens, 100 chunk = 5 per pass)
	for i := 0; i < 20; i++ {
		s.AppendMessage("user", "message content here", `{}`, 20)
	}

	if err := c.fullSweep(ctx); err != nil {
		t.Fatalf("fullSweep: %v", err)
	}

	// Should have created multiple summaries
	if fs.calls < 2 {
		t.Errorf("expected multiple summarizer calls, got %d", fs.calls)
	}

	// Verify total context tokens are reduced
	total, _ := s.ContextTokenCount()
	t.Logf("after full sweep: %d total tokens, %d summarizer calls", total, fs.calls)
}
