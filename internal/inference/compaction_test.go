package inference

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
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

	// Pass real input_tokens below soft threshold — no compaction.
	result, err := compactor.CompactIfNeeded(context.Background(), 50_000)
	if err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if summarizer.calls > 0 {
		t.Error("summarizer should not be called below threshold")
	}
	if result.HardThresholdExceeded {
		t.Error("hard threshold should not be exceeded")
	}
}

func TestCompactIfNeeded_LeafPassTriggered(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		ContextWindow:        10_000,
		SoftThreshold:        0.60, // soft = 6000
		HardThreshold:        0.85,
		TokenBudget:          9_000,
		FreshTailCount:       5,
		LeafChunkTokens:      500, // low threshold to trigger leaf pass inside sweep
		LeafTargetTokens:     200,
		CondenseTargetTokens: 400,
		LeafMinFanout:        3,
		CondenseMinFanout:    4,
	}

	// Add enough messages outside the fresh tail to trigger leaf pass.
	for i := 0; i < 20; i++ {
		appendMsg(t, pdb, "s1", "user", "message for compaction testing", 100)
	}

	summarizer := &fakeSummarizer{results: []string{"leaf summary of messages"}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	// Report real input_tokens above soft threshold to trigger full sweep.
	if _, err := compactor.CompactIfNeeded(context.Background(), 7_000); err != nil {
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

func TestCompactIfNeeded_SoftThresholdTriggersFullSweep(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		ContextWindow:        200_000,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          150_000,
		FreshTailCount:       5,
		LeafChunkTokens:      500,
		LeafTargetTokens:     200,
		CondenseTargetTokens: 400,
		LeafMinFanout:        3,
		CondenseMinFanout:    4,
	}

	// Add messages — their estimated tokens don't matter for the trigger,
	// but they need to exist for the leaf pass to have material to compact.
	for i := 0; i < 20; i++ {
		appendMsg(t, pdb, "s1", "user", "message for compaction testing", 100)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Below soft threshold (120K): no compaction.
	belowSummarizer := &fakeSummarizer{}
	belowCompactor := NewCompactor(pdb, "s1", belowSummarizer, cfg, logger)
	result, err := belowCompactor.CompactIfNeeded(context.Background(), 100_000)
	if err != nil {
		t.Fatalf("CompactIfNeeded below soft: %v", err)
	}
	if belowSummarizer.calls > 0 {
		t.Error("expected no compaction below soft threshold")
	}
	if result.HardThresholdExceeded {
		t.Error("hard threshold should not be exceeded below soft")
	}

	// Above soft threshold (120K) but below hard (170K): full sweep runs.
	aboveSummarizer := &fakeSummarizer{results: []string{"compacted summary"}}
	aboveCompactor := NewCompactor(pdb, "s1", aboveSummarizer, cfg, logger)
	result, err = aboveCompactor.CompactIfNeeded(context.Background(), 130_000)
	if err != nil {
		t.Fatalf("CompactIfNeeded above soft: %v", err)
	}
	if aboveSummarizer.calls == 0 {
		t.Error("expected full sweep to be triggered at soft threshold")
	}
	if result.HardThresholdExceeded {
		t.Error("hard threshold should not be exceeded at 130K")
	}
}

func TestCompactIfNeeded_HardThresholdSetsFlag(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Use a small context window so we can exceed the hard threshold with
	// estimated tokens that remain high even after compaction. With a 10K
	// window, hard threshold = 8500. We put most tokens in the fresh tail
	// where compaction can't touch them.
	cfg := CompactionConfig{
		ContextWindow:        10_000,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          7_500,
		FreshTailCount:       5,
		LeafChunkTokens:      500,
		LeafTargetTokens:     200,
		CondenseTargetTokens: 400,
		LeafMinFanout:        3,
		CondenseMinFanout:    4,
	}

	// Add 10 messages: 5 old (compactable) + 5 in fresh tail.
	// Each message is 2000 estimated tokens. Fresh tail alone = 10K,
	// which exceeds the hard threshold (8500) even after compacting the old ones.
	for i := 0; i < 10; i++ {
		appendMsg(t, pdb, "s1", "user", "message for compaction testing", 2000)
	}

	summarizer := &fakeSummarizer{results: []string{"short summary"}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	// Report 9K real input_tokens — above hard threshold (8500).
	result, err := compactor.CompactIfNeeded(context.Background(), 9_000)
	if err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if !result.HardThresholdExceeded {
		t.Error("expected hard threshold to still be exceeded — fresh tail alone exceeds it")
	}

	// Verify the mechanical assumption: post-compaction estimated tokens
	// must actually exceed the hard threshold for this test to be meaningful.
	postTokens, err := pdb.ContextTokenCount("s1")
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}
	if postTokens < cfg.HardThresholdTokens() {
		t.Errorf("post-compaction tokens (%d) below hard threshold (%d) — test premise is broken",
			postTokens, cfg.HardThresholdTokens())
	}
}

func TestCompactIfNeeded_FallsBackToEstimatesWhenNoAPIData(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		ContextWindow:        200_000,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          180_000,
		FreshTailCount:       5,
		LeafChunkTokens:      50_000, // high so leaf pass doesn't trigger inside sweep
		LeafTargetTokens:     200,
		CondenseTargetTokens: 400,
		LeafMinFanout:        3,
		CondenseMinFanout:    4,
	}

	// Add messages with estimated tokens that exceed soft threshold (120K).
	for i := 0; i < 20; i++ {
		appendMsg(t, pdb, "s1", "user", "message content", 7000)
	}

	summarizer := &fakeSummarizer{results: []string{"compacted summary"}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	// Pass 0 for lastInputTokens — should fall back to estimated sum (140K).
	_, err := compactor.CompactIfNeeded(context.Background(), 0)
	if err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}
	if summarizer.calls == 0 {
		t.Error("expected full sweep from estimated tokens exceeding soft threshold")
	}
}

func TestCompactIfNeeded_CondensationFiresAfterLeafPasses(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		ContextWindow:        10_000,
		SoftThreshold:        0.01, // force compaction
		HardThreshold:        0.85,
		TokenBudget:          9_000,
		FreshTailCount:       2,
		LeafChunkTokens:      500, // must be >= LeafMinFanout * per-message tokens
		LeafTargetTokens:     50,
		CondenseTargetTokens: 100,
		LeafMinFanout:        3,
		CondenseMinFanout:    3,
	}

	// Add 30 messages — enough for multiple leaf passes that produce 3+
	// adjacent summaries, which should trigger condensation.
	for i := 0; i < 30; i++ {
		appendMsg(t, pdb, "s1", "user", "message for condensation test", 100)
	}

	// Provide enough canned summaries for leaf passes + condensation.
	summarizer := &fakeSummarizer{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	_, err := compactor.CompactIfNeeded(context.Background(), 500)
	if err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}

	maxDepth, err := pdb.MaxSummaryDepth("s1")
	if err != nil {
		t.Fatalf("MaxSummaryDepth: %v", err)
	}
	if maxDepth < 1 {
		// Log context items for debugging.
		items, _ := pdb.GetContextItems("s1")
		for _, item := range items {
			t.Logf("  ordinal=%d type=%s msg=%v sum=%v", item.Ordinal, item.ItemType, item.MessageID, item.SummaryID)
		}
		t.Errorf("expected condensation (depth >= 1), got max depth %d", maxDepth)
	}
}

func TestPrecedingSummaryContent_FlowsThroughToPrompt(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	cfg := CompactionConfig{
		ContextWindow:        10_000,
		SoftThreshold:        0.01, // force compaction
		HardThreshold:        0.99,
		TokenBudget:          9_000,
		FreshTailCount:       2,
		LeafChunkTokens:      400, // fits 4 messages at 100 tokens each
		LeafTargetTokens:     200,
		CondenseTargetTokens: 400,
		LeafMinFanout:        3,
		CondenseMinFanout:    100, // prevent condensation
	}

	// Add 10 messages: 8 outside fresh tail, enough for two leaf passes
	// (4 messages per pass at 100 tokens each, LeafChunkTokens=400).
	for i := 0; i < 10; i++ {
		appendMsg(t, pdb, "s1", "user", "message for dedup test", 100)
	}

	// Track all system prompts sent to the summarizer.
	summarizer := &capturingSummarizer{
		result: "- fact one about the topic\n- fact two about the details",
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	compactor := NewCompactor(pdb, "s1", summarizer, cfg, logger)

	if _, err := compactor.CompactIfNeeded(context.Background(), 500); err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}

	if len(summarizer.prompts) < 2 {
		t.Fatalf("expected at least 2 summarizer calls, got %d", len(summarizer.prompts))
	}

	// First leaf pass should NOT have previous_context (nothing precedes it).
	if strings.Contains(summarizer.prompts[0], "<previous_context>") {
		t.Error("first leaf pass should not have previous_context")
	}

	// Second leaf pass SHOULD have previous_context from the first summary.
	if !strings.Contains(summarizer.prompts[1], "<previous_context>") {
		t.Error("second leaf pass should include previous_context from first summary")
	}
	if !strings.Contains(summarizer.prompts[1], "fact one about the topic") {
		t.Error("previous_context should contain the first summary's content")
	}
}

// capturingSummarizer records the system prompt of each call for assertion.
type capturingSummarizer struct {
	prompts []string
	result  string
}

func (s *capturingSummarizer) Summarize(_ context.Context, systemPrompt, _ string) (string, error) {
	s.prompts = append(s.prompts, systemPrompt)
	return s.result, nil
}

func TestSummarizationPrompt_Variants(t *testing.T) {
	tests := []struct {
		depth      int
		aggressive bool
		contains   string
	}{
		{0, false, "maximum fidelity"},
		{0, true, "tight space budget"},
		{1, false, "Merge these conversation summaries"},
		{1, true, "tight space budget"},
		{2, false, "trajectory, outcomes, and durable constraints"},
		{2, true, "trajectory, outcomes, and durable constraints"},
		{3, false, "high-level memory"},
		{3, true, "durable context only"},
	}
	for _, tt := range tests {
		got := summarizationPrompt(tt.depth, tt.aggressive, 500, "")
		if !strings.Contains(got, tt.contains) {
			t.Errorf("depth=%d aggressive=%v: missing %q in:\n%s", tt.depth, tt.aggressive, tt.contains, got)
		}
		// All prompts should include the budget hint.
		if !strings.Contains(got, "500 tokens") {
			t.Errorf("depth=%d aggressive=%v: missing budget hint", tt.depth, tt.aggressive)
		}
		// All prompts should include the expand footer.
		if !strings.Contains(got, "Expand for details about") {
			t.Errorf("depth=%d aggressive=%v: missing expand footer", tt.depth, tt.aggressive)
		}
	}
}

func TestSummarizationPrompt_PreviousContext(t *testing.T) {
	// Without previous context — no dedup section.
	got := summarizationPrompt(0, false, 500, "")
	if strings.Contains(got, "previous_context") {
		t.Error("should not include previous_context when empty")
	}

	// With previous context — dedup section present.
	got = summarizationPrompt(0, false, 500, "- Jon lost his job on 2023-01-19")
	if !strings.Contains(got, "<previous_context>") {
		t.Error("should include previous_context XML tag")
	}
	if !strings.Contains(got, "do not repeat or restate facts") {
		t.Error("should include dedup instruction")
	}
	if !strings.Contains(got, "Jon lost his job") {
		t.Error("should include the actual previous context content")
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

func TestTruncateAtBullet(t *testing.T) {
	short := "hello"
	if got := truncateAtBullet(short, 100); got != short {
		t.Errorf("short string should not be truncated")
	}

	// Build a bullet-formatted summary.
	bullets := "- First fact about the topic\n- Second fact with details\n- Third fact is important\n- Fourth fact at the end"
	got := truncateAtBullet(bullets, 20) // 20 tokens = 80 chars
	if len(got) > 80 {
		t.Errorf("truncated result too long: %d chars", len(got))
	}
	// Should end at a bullet boundary, not mid-sentence.
	if strings.Contains(got, "Fourth") {
		t.Error("should not include the last bullet that doesn't fit")
	}
	if !strings.HasPrefix(got, "- First") {
		t.Error("should preserve first bullet")
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
	got, err := compactor.summarizeWithEscalation(context.Background(), 0, "input text", "")
	if err != nil {
		t.Fatalf("summarizeWithEscalation: %v", err)
	}

	// With 1.5x tolerance, 6250 tokens still far exceeds 1.5x target, so
	// truncation should kick in. Result should be at or under target since
	// truncateAtBullet cuts to fit.
	target := DefaultCompactionConfig().LeafTargetTokens
	if EstimateTokens(got) > target+10 {
		t.Errorf("result tokens %d exceeds target %d", EstimateTokens(got), target)
	}
	if summarizer.calls != 2 {
		t.Errorf("expected 2 summarizer calls (normal + aggressive), got %d", summarizer.calls)
	}
}

func TestCompactionConfigScaling(t *testing.T) {
	tests := []struct {
		window         int
		wantChunk      int
		wantLeafTarget int
		wantCondTarget int
		wantTail       int
		wantCondFanout int
		wantSoft       int
		wantHard       int
		wantBudget     int
		wantMaxDepth   int
	}{
		{32_000, 3_200, 640, 1_280, 20, 3, 19_200, 27_200, 28_800, 4},           // small: linear scaling
		{200_000, 20_000, 4_000, 8_000, 20, 3, 120_000, 170_000, 180_000, 4},    // reference: caps equal linear
		{340_000, 20_000, 4_000, 8_000, 34, 3, 200_000, 289_000, 306_000, 6},    // transition: soft capped, hard still linear
		{500_000, 20_000, 4_000, 8_000, 50, 5, 200_000, 300_000, 350_000, 8},    // large: all caps active
		{1_000_000, 20_000, 4_000, 8_000, 100, 6, 200_000, 300_000, 350_000, 8}, // 1M: capped
		{2_000_000, 20_000, 4_000, 8_000, 200, 6, 200_000, 300_000, 350_000, 8}, // 2M: capped
	}
	for _, tt := range tests {
		cfg := compactionConfigForWindow(tt.window)
		if cfg.LeafChunkTokens != tt.wantChunk {
			t.Errorf("window=%d: LeafChunkTokens=%d, want %d", tt.window, cfg.LeafChunkTokens, tt.wantChunk)
		}
		if cfg.LeafTargetTokens != tt.wantLeafTarget {
			t.Errorf("window=%d: LeafTargetTokens=%d, want %d", tt.window, cfg.LeafTargetTokens, tt.wantLeafTarget)
		}
		if cfg.CondenseTargetTokens != tt.wantCondTarget {
			t.Errorf("window=%d: CondenseTargetTokens=%d, want %d", tt.window, cfg.CondenseTargetTokens, tt.wantCondTarget)
		}
		if cfg.FreshTailCount != tt.wantTail {
			t.Errorf("window=%d: FreshTailCount=%d, want %d", tt.window, cfg.FreshTailCount, tt.wantTail)
		}
		if cfg.CondenseMinFanout != tt.wantCondFanout {
			t.Errorf("window=%d: CondenseMinFanout=%d, want %d", tt.window, cfg.CondenseMinFanout, tt.wantCondFanout)
		}
		if soft := cfg.SoftThresholdTokens(); soft != tt.wantSoft {
			t.Errorf("window=%d: SoftThresholdTokens=%d, want %d", tt.window, soft, tt.wantSoft)
		}
		if hard := cfg.HardThresholdTokens(); hard != tt.wantHard {
			t.Errorf("window=%d: HardThresholdTokens=%d, want %d", tt.window, hard, tt.wantHard)
		}
		if cfg.TokenBudget != tt.wantBudget {
			t.Errorf("window=%d: TokenBudget=%d, want %d", tt.window, cfg.TokenBudget, tt.wantBudget)
		}
		if cfg.MaxSummaryDepth != tt.wantMaxDepth {
			t.Errorf("window=%d: MaxSummaryDepth=%d, want %d", tt.window, cfg.MaxSummaryDepth, tt.wantMaxDepth)
		}
	}
}

func TestGenerateSummaryID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateSummaryID()
		if !strings.HasPrefix(id, "sum_") {
			t.Fatalf("id %q missing sum_ prefix", id)
		}
		// "sum_" (4) + 16 hex chars = 20 total.
		if len(id) != 20 {
			t.Fatalf("id %q has length %d, want 20", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d generations", id, i)
		}
		seen[id] = true
	}
}
