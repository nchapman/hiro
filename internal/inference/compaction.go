package inference

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"

	"github.com/google/uuid"

	"github.com/nchapman/hivebot/internal/models"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
)

// Summarizer generates summaries from text.
type Summarizer interface {
	Summarize(ctx context.Context, systemPrompt, input string) (string, error)
}

// lmSummarizer adapts a fantasy.LanguageModel to the Summarizer interface.
type lmSummarizer struct {
	lm fantasy.LanguageModel
}

func (s *lmSummarizer) Summarize(ctx context.Context, systemPrompt, input string) (string, error) {
	resp, err := s.lm.Generate(ctx, fantasy.Call{
		Prompt: fantasy.Prompt{
			fantasy.NewSystemMessage(systemPrompt),
			fantasy.NewUserMessage(input),
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Content.Text(), nil
}

// CompactionConfig controls compaction behavior.
//
// Token thresholds use two types of counts:
//   - Estimated tokens (len/4): stored per-message, used for relative sizing
//     (chunk boundaries, compression ratios). Fine for internal bookkeeping.
//   - Actual input_tokens from the API: used for trigger decisions (soft/hard
//     thresholds) since these reflect the real context window consumption.
//
// The soft/hard threshold model follows the LCM paper (Ehrlich & Blackman, 2026):
//   - Below SoftThreshold: no compaction overhead.
//   - Between SoftThreshold and HardThreshold: async compaction between turns.
//   - At or above HardThreshold: synchronous compaction before the next turn.
type CompactionConfig struct {
	// ContextWindow is the model's full context window in tokens.
	ContextWindow int

	// SoftThreshold is the fraction of ContextWindow at which async compaction
	// triggers. Compared against real input_tokens from the API.
	SoftThreshold float64

	// HardThreshold is the fraction of ContextWindow at which synchronous
	// compaction is required before the next turn. Compared against real
	// input_tokens from the API.
	HardThreshold float64

	// TokenBudget is the assembly budget in estimated tokens. Used as a loose
	// safety net during context assembly — the real protection is the hard
	// threshold. Set to ContextWindow * 0.90 by default (above HardThreshold
	// so it only clips in edge cases where compaction couldn't free enough).
	TokenBudget int

	FreshTailCount       int
	LeafChunkTokens      int
	LeafTargetTokens     int
	CondenseTargetTokens int
	LeafMinFanout        int
	CondenseMinFanout    int
}

// SoftThresholdTokens returns the absolute token count for the soft threshold.
func (c CompactionConfig) SoftThresholdTokens() int {
	return int(float64(c.ContextWindow) * c.SoftThreshold)
}

// HardThresholdTokens returns the absolute token count for the hard threshold.
func (c CompactionConfig) HardThresholdTokens() int {
	return int(float64(c.ContextWindow) * c.HardThreshold)
}

// DefaultCompactionConfig returns reasonable defaults for a 200K context window.
func DefaultCompactionConfig() CompactionConfig {
	return compactionConfigForWindow(200_000)
}

// CompactionConfigForModel returns a config derived from the model's context window.
// All size-dependent parameters scale proportionally so compaction behaves
// consistently whether the model has 32K or 1M tokens.
func CompactionConfigForModel(model string) CompactionConfig {
	return compactionConfigForWindow(models.ContextWindow(model))
}

// compactionConfigForWindow builds a config where all parameters are derived
// from the context window size. This ensures consistent compression ratios
// and behavior across models of any size.
//
// The key ratios:
//   - Leaf chunk: 10% of window — how much to grab per summarization pass
//   - Leaf target: 2% of window — ~5:1 compression at leaf level
//   - Condense target: 3% of window — ~3:1 compression when combining summaries
//   - Token budget: 90% of window — loose assembly safety net
//
// At 200K: chunk=20K, leaf_target=4K, condense_target=6K
// At  32K: chunk=3.2K, leaf_target=640, condense_target=960
// At   1M: chunk=100K, leaf_target=20K, condense_target=30K
func compactionConfigForWindow(contextWindow int) CompactionConfig {
	if contextWindow <= 0 {
		contextWindow = 200_000
	}
	return CompactionConfig{
		ContextWindow:        contextWindow,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          max(1_000, contextWindow*9/10),
		FreshTailCount:       20,
		LeafChunkTokens:      max(500, contextWindow/10),
		LeafTargetTokens:     max(200, contextWindow/50),
		CondenseTargetTokens: max(300, contextWindow*3/100),
		LeafMinFanout:        4,
		CondenseMinFanout:    3,
	}
}

// Compactor runs incremental compaction on conversation history.
type Compactor struct {
	pdb        *platformdb.DB
	sessionID  string
	summarizer Summarizer
	config     CompactionConfig
	logger     *slog.Logger
}

// NewCompactor creates a new compaction engine.
func NewCompactor(pdb *platformdb.DB, sessionID string, summarizer Summarizer, cfg CompactionConfig, logger *slog.Logger) *Compactor {
	return &Compactor{
		pdb:        pdb,
		sessionID:  sessionID,
		summarizer: summarizer,
		config:     cfg,
		logger:     logger,
	}
}

// CompactResult reports what happened during compaction.
type CompactResult struct {
	// HardThresholdExceeded is true if the context still exceeds the hard
	// threshold after compaction. The caller should run synchronous compaction
	// before the next inference call.
	HardThresholdExceeded bool
}

// CompactIfNeeded runs incremental compaction based on real API token usage.
//
// lastInputTokens is the input_tokens reported by the provider for the last
// inference step — the ground truth for how full the context window is. When
// zero (e.g., first turn or no usage data), falls back to estimated token
// counts from the database.
func (c *Compactor) CompactIfNeeded(ctx context.Context, lastInputTokens int64) (CompactResult, error) {
	var result CompactResult

	// Use real input_tokens for threshold comparison when available.
	// Fall back to estimated ContextTokenCount when we have no API data
	// (first turn, or usage not tracked).
	contextTokens := lastInputTokens
	if contextTokens == 0 {
		estimated, err := c.pdb.ContextTokenCount(c.sessionID)
		if err != nil {
			return result, fmt.Errorf("checking context tokens: %w", err)
		}
		contextTokens = int64(estimated)
	}

	softLimit := int64(c.config.SoftThresholdTokens())
	hardLimit := int64(c.config.HardThresholdTokens())

	// Below the soft threshold: no compaction overhead (LCM zero-cost regime).
	if contextTokens < softLimit {
		return result, nil
	}

	// At or above soft threshold: run full sweep (leaf passes + condensation).
	c.logger.Info("compaction triggered",
		"context_tokens", contextTokens,
		"soft_limit", softLimit,
		"hard_limit", hardLimit,
		"source", tokenSource(lastInputTokens),
	)
	if err := c.fullSweep(ctx); err != nil {
		return result, fmt.Errorf("full sweep: %w", err)
	}

	// Re-check with post-compaction estimated tokens to decide if the hard
	// threshold is still exceeded. We use estimates here because the real
	// input_tokens won't be known until the next API call.
	postTokens, err := c.pdb.ContextTokenCount(c.sessionID)
	if err != nil {
		return result, fmt.Errorf("post-compaction token check: %w", err)
	}
	if int64(postTokens) >= hardLimit {
		result.HardThresholdExceeded = true
		c.logger.Warn("context still exceeds hard threshold after compaction",
			"post_tokens_estimated", postTokens,
			"hard_limit", hardLimit,
		)
	}

	return result, nil
}

func tokenSource(lastInputTokens int64) string {
	if lastInputTokens > 0 {
		return "api"
	}
	return "estimated"
}

func (c *Compactor) leafPass(ctx context.Context) error {
	items, msgs, err := c.pdb.OldestMessageContextItems(c.sessionID, c.config.FreshTailCount, c.config.LeafChunkTokens)
	if err != nil {
		return err
	}
	if len(msgs) < c.config.LeafMinFanout {
		return nil
	}

	input := buildLeafInput(msgs)
	sourceTokens := 0
	var msgIDs []int64
	for _, m := range msgs {
		sourceTokens += m.Tokens
		msgIDs = append(msgIDs, m.ID)
	}

	summary, err := c.summarizeWithEscalation(ctx, 0, input, sourceTokens)
	if err != nil {
		return err
	}

	summaryID := generateSummaryID()
	summaryTokens := EstimateTokens(summary)

	sum := platformdb.Summary{
		ID:           summaryID,
		SessionID:    c.sessionID,
		Kind:         "leaf",
		Depth:        0,
		Content:      summary,
		Tokens:       summaryTokens,
		EarliestAt:   msgs[0].CreatedAt,
		LatestAt:     msgs[len(msgs)-1].CreatedAt,
		SourceTokens: sourceTokens,
	}

	if err := c.pdb.CreateSummary(sum); err != nil {
		return fmt.Errorf("creating summary: %w", err)
	}
	if err := c.pdb.LinkSummaryMessages(summaryID, msgIDs); err != nil {
		return fmt.Errorf("linking messages: %w", err)
	}
	if err := c.pdb.ReplaceContextItems(c.sessionID, items[0].Ordinal, items[len(items)-1].Ordinal, summaryID); err != nil {
		return fmt.Errorf("replacing context items: %w", err)
	}

	c.logger.Info("leaf compaction complete",
		"summary_id", summaryID,
		"messages", len(msgs),
		"source_tokens", sourceTokens,
		"summary_tokens", summaryTokens,
	)
	return nil
}

func (c *Compactor) condensationPass(ctx context.Context) (bool, error) {
	maxDepth, err := c.pdb.MaxSummaryDepth(c.sessionID)
	if err != nil {
		return false, err
	}
	if maxDepth < 0 {
		return false, nil
	}

	for depth := 0; depth <= maxDepth; depth++ {
		items, sums, err := c.pdb.ContiguousSummariesAtDepth(c.sessionID, depth, c.config.CondenseMinFanout)
		if err != nil {
			return false, err
		}
		if len(sums) == 0 {
			continue
		}

		input := buildCondensationInput(sums)
		sourceTokens := 0
		var childIDs []string
		for _, s := range sums {
			sourceTokens += s.Tokens
			childIDs = append(childIDs, s.ID)
		}

		summary, err := c.summarizeWithEscalation(ctx, depth+1, input, sourceTokens)
		if err != nil {
			return false, err
		}

		summaryID := generateSummaryID()
		summaryTokens := EstimateTokens(summary)

		sum := platformdb.Summary{
			ID:           summaryID,
			SessionID:    c.sessionID,
			Kind:         "condensed",
			Depth:        depth + 1,
			Content:      summary,
			Tokens:       summaryTokens,
			EarliestAt:   sums[0].EarliestAt,
			LatestAt:     sums[len(sums)-1].LatestAt,
			SourceTokens: sourceTokens,
		}

		if err := c.pdb.CreateSummary(sum); err != nil {
			return false, fmt.Errorf("creating condensed summary: %w", err)
		}
		if err := c.pdb.LinkSummaryParents(summaryID, childIDs); err != nil {
			return false, fmt.Errorf("linking parents: %w", err)
		}
		if err := c.pdb.ReplaceContextItems(c.sessionID, items[0].Ordinal, items[len(items)-1].Ordinal, summaryID); err != nil {
			return false, fmt.Errorf("replacing context items: %w", err)
		}

		c.logger.Info("condensation complete",
			"summary_id", summaryID,
			"depth", depth+1,
			"inputs", len(sums),
			"source_tokens", sourceTokens,
			"summary_tokens", summaryTokens,
		)
		return true, nil
	}
	return false, nil
}

func (c *Compactor) fullSweep(ctx context.Context) error {
	const maxIterations = 10
	for i := 0; i < maxIterations; i++ {
		tokensOutside, err := c.pdb.MessageTokensOutsideTail(c.sessionID, c.config.FreshTailCount)
		if err != nil {
			return err
		}
		if tokensOutside >= c.config.LeafChunkTokens {
			if err := c.leafPass(ctx); err != nil {
				return err
			}
			continue
		}

		condensed, err := c.condensationPass(ctx)
		if err != nil {
			return err
		}
		if condensed {
			continue
		}
		break
	}
	return nil
}

func (c *Compactor) summarizeWithEscalation(ctx context.Context, depth int, input string, sourceTokens int) (string, error) {
	targetTokens := c.config.LeafTargetTokens
	if depth > 0 {
		targetTokens = c.config.CondenseTargetTokens
	}

	prompt := summarizationPrompt(depth, false)
	summary, err := c.summarizer.Summarize(ctx, prompt, input)
	if err != nil {
		return "", fmt.Errorf("normal summarization: %w", err)
	}

	summaryTokens := EstimateTokens(summary)
	if summaryTokens <= targetTokens {
		return summary, nil
	}

	c.logger.Info("escalating to aggressive summarization",
		"summary_tokens", summaryTokens,
		"target_tokens", targetTokens,
	)
	prompt = summarizationPrompt(depth, true)
	summary, err = c.summarizer.Summarize(ctx, prompt, input)
	if err != nil {
		return "", fmt.Errorf("aggressive summarization: %w", err)
	}

	summaryTokens = EstimateTokens(summary)
	if summaryTokens <= targetTokens {
		return summary, nil
	}

	c.logger.Warn("falling back to deterministic truncation",
		"summary_tokens", summaryTokens,
		"target_tokens", targetTokens,
	)
	return fallbackTruncate(summary, targetTokens), nil
}

func generateSummaryID() string {
	id := strings.ReplaceAll(uuid.New().String(), "-", "")
	return "sum_" + id[:16]
}

// --- Prompt/formatting helpers ---

func summarizationPrompt(depth int, aggressive bool) string {
	switch {
	case depth == 0 && !aggressive:
		return leafPrompt
	case depth == 0 && aggressive:
		return leafAggressivePrompt
	case !aggressive:
		return condensePrompt
	default:
		return condenseAggressivePrompt
	}
}

// leafPrompt extracts structured facts from raw conversation messages.
// Optimized for maximum fact density per token — every bullet should be
// independently useful for answering future questions.
const leafPrompt = `You are a fact extractor for a conversation memory system. Your output will be the ONLY record of this conversation segment — any fact you omit is permanently lost.

Extract ALL retrievable facts as structured bullet points.

## Rules

1. DATES: Convert every relative time reference ("yesterday", "last week", "2 days ago") to an absolute date using the message timestamps. If a message dated 2024-03-15 says "yesterday", write "2024-03-14". If a message dated 2024-06-10 says "last year", write "2023".

2. PRESERVE EXACTLY: names, places, file paths, URLs, commands, error messages, version numbers, quantities, code snippets, and technical identifiers. Never paraphrase these — copy them verbatim.

3. ATTRIBUTION: Note who said or did what. Speaker identity matters for future questions like "what did X do?" or "what does Y think about Z?"

4. DECISIONS: For each decision, state WHAT was decided AND WHY (the reason or constraint that drove it).

5. RELATIONSHIPS AND STATES: Capture personal details (occupation, location, relationships, preferences, plans, opinions, identity) — these are high-value for future recall.

6. COMPLETENESS: If someone mentions a specific fact — a place they visited, a date something happened, a preference they have, a plan they're making — it MUST appear in your output. Err on the side of including too much rather than too little.

## Output Format

Write concise bullet points grouped by topic. Each bullet = one self-contained fact.

DO NOT write narrative prose or connecting sentences.
DO NOT start with "The conversation covered..." or similar framing.
DO NOT editorialize or add interpretation beyond what was stated.`

// leafAggressivePrompt is the escalation when normal leaf output exceeds
// the token target. Re-extracts from source with tighter constraints.
const leafAggressivePrompt = `You are a conversation compressor. Extract only the essential facts as terse bullet points.

Rules:
1. Convert all relative dates to absolute dates using the timestamps shown.
2. KEEP: decisions (what + why), outcomes, names, dates, file paths, error messages, version numbers, quantities, personal details (identity, relationships, plans).
3. DROP: discussion, reasoning, greetings, exploration that led nowhere, pleasantries.
4. Each bullet: one fact, max ~15 words. No narrative, no framing sentences.`

// condensePrompt merges multiple leaf summaries into a unified overview.
const condensePrompt = `You are merging conversation summaries into a unified record. The input summaries were extracted from consecutive segments of the same conversation.

## Rules

1. MERGE related facts: combine information about the same topic, person, or decision from different summaries into single comprehensive bullets.
2. PRESERVE ALL specific details: dates, names, file paths, versions, quantities, error messages, and personal facts. If a fact appears in any input summary, it must appear in your output.
3. ELIMINATE only strict duplicates — the same fact stated identically in multiple summaries.
4. MAINTAIN chronological anchoring: keep dates attached to their facts.
5. GROUP by topic or entity for coherence.

Write concise bullet points. Do not add narrative framing. Density matters more than brevity — include every fact from the inputs.`

// condenseAggressivePrompt is the escalation for condensation.
const condenseAggressivePrompt = `Compress these conversation summaries into a compact factual record.

Keep all dates, names, file paths, identifiers, quantities, decisions with rationale, and personal details.
Merge related facts aggressively. Terse bullet points only.`

func buildLeafInput(msgs []platformdb.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] %s: %s\n\n",
			m.CreatedAt.Format("2006-01-02 15:04:05"),
			m.Role,
			m.Content,
		)
	}
	return strings.TrimSpace(b.String())
}

func buildCondensationInput(sums []platformdb.Summary) string {
	var b strings.Builder
	for i, s := range sums {
		fmt.Fprintf(&b, "--- Summary %d (depth %d, %s to %s) ---\n%s\n\n",
			i+1, s.Depth,
			s.EarliestAt.Format("2006-01-02 15:04"),
			s.LatestAt.Format("2006-01-02 15:04"),
			s.Content,
		)
	}
	return strings.TrimSpace(b.String())
}

func fallbackTruncate(content string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n[Truncated for context management]"
}

