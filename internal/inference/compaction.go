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
//   - Condense target: 4% of window — ~2.5:1 compression when combining summaries
//   - Token budget: 90% of window — loose assembly safety net
//
// At 200K: chunk=20K, leaf_target=4K, condense_target=8K
// At  32K: chunk=3.2K, leaf_target=640, condense_target=1.3K
// At   1M: chunk=100K, leaf_target=20K, condense_target=40K
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
		CondenseTargetTokens: max(400, contextWindow*4/100),
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

	prompt := summarizationPrompt(depth, false, targetTokens)
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
	prompt = summarizationPrompt(depth, true, targetTokens)
	summary, err = c.summarizer.Summarize(ctx, prompt, input)
	if err != nil {
		return "", fmt.Errorf("aggressive summarization: %w", err)
	}

	summaryTokens = EstimateTokens(summary)
	// Allow up to 1.5x target before truncating — the model tried its best,
	// and modest overshoot preserves more facts than hard truncation.
	if summaryTokens <= targetTokens*3/2 {
		return summary, nil
	}

	c.logger.Warn("falling back to deterministic truncation",
		"summary_tokens", summaryTokens,
		"target_tokens", targetTokens,
	)
	return truncateAtBullet(summary, targetTokens), nil
}

func generateSummaryID() string {
	id := strings.ReplaceAll(uuid.New().String(), "-", "")
	return "sum_" + id[:16]
}

// --- Prompt/formatting helpers ---

func summarizationPrompt(depth int, aggressive bool, targetTokens int) string {
	budgetHint := fmt.Sprintf("\n\nBudget: ~%d tokens (~%d characters). Prioritize the most important facts if you cannot fit everything.", targetTokens, targetTokens*4)

	var base string
	switch {
	case depth == 0 && !aggressive:
		base = leafPrompt
	case depth == 0 && aggressive:
		base = leafAggressivePrompt
	case !aggressive:
		base = condensePrompt
	default:
		base = condenseAggressivePrompt
	}
	return base + budgetHint
}

// leafPrompt summarizes raw conversation messages into bullet points.
const leafPrompt = `Objective: Preserve maximum fidelity in minimum words. The original messages will be deleted — your summary is the only record. Someone will later use it to answer questions about this conversation.

Keep: facts, decisions (what AND why), opinions, feelings, dates, names, numbers, identifiers, personal details, reasons behind actions. Attribute who said/did/felt what. Copy names, paths, commands, error messages, and numbers verbatim. Every event must have a date — "when" questions are very common.

Discard: greetings, filler, pleasantries, and anything already established in an earlier bullet.

Format: one bullet per fact, concise. Convert relative dates using the message timestamps ("yesterday" on 2024-03-15 → 2024-03-14). Match the precision of the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. State a person's identity/role once, then use their name only in subsequent bullets. No prose or framing sentences.`

// leafAggressivePrompt is the escalation when normal leaf output exceeds
// the token target. Same objective, much tighter budget.
const leafAggressivePrompt = `Objective: Preserve maximum fidelity in minimum words. The original messages will be deleted — your summary is the only record. You are on a very tight space budget.

Keep: facts, decisions (what AND why), opinions, feelings, dates, names, numbers, identifiers, personal details, reasons. Copy names, paths, commands, error messages, and numbers verbatim. Every event must have a date.

Discard: greetings, filler, restated information, context already established in earlier bullets.

Format: one fact per bullet, ~15 words max. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. State a person's identity/role once; use name only after. No prose.`

// condensePrompt merges multiple summaries into one.
const condensePrompt = `Objective: Merge these conversation summaries into one record with maximum fidelity in minimum words. The inputs are from consecutive segments. Your output replaces all of them.

Keep: every distinct fact from any input. Merge related facts about the same topic or person into single bullets where possible. Preserve the date for every event — never merge away a date.

Discard: strict duplicates, redundant phrasing, repeated context (e.g. a person's role restated across inputs — keep it once).

Format: bullet points grouped by person or topic. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. No prose or framing sentences.`

// condenseAggressivePrompt is the tight-budget version of condensation.
const condenseAggressivePrompt = `Objective: Merge these conversation summaries with maximum fidelity in minimum words. Your output replaces all inputs — anything you drop becomes permanently unanswerable. Very tight space budget.

Keep: every distinct fact. Merge related facts aggressively. Preserve the date for every event.

Discard: duplicates, redundant phrasing, repeated context.

Format: terse bullet points. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. No prose.`

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

// truncateAtBullet truncates content at the last complete bullet point that
// fits within the token budget. This preserves whole facts rather than
// chopping mid-sentence.
func truncateAtBullet(content string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content
	}

	// Find the last complete bullet point that fits within budget.
	truncated := content[:maxChars]
	if lastBullet := strings.LastIndex(truncated, "\n- "); lastBullet > 0 {
		truncated = truncated[:lastBullet]
	} else if lastSpace := strings.LastIndexByte(truncated, ' '); lastSpace > 0 {
		// No bullet pattern — fall back to last word boundary.
		truncated = truncated[:lastSpace]
	}

	return strings.TrimRight(truncated, "\n ")
}

