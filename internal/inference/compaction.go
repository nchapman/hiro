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
	return CompactionConfig{
		ContextWindow:        200_000,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          180_000, // 90% of 200K — above hard threshold, loose safety net
		FreshTailCount:       20,
		LeafChunkTokens:      20_000,
		LeafTargetTokens:     1_200,
		CondenseTargetTokens: 2_000,
		LeafMinFanout:        8,
		CondenseMinFanout:    4,
	}
}

// CompactionConfigForModel returns a config derived from the model's context window.
// Thresholds that depend on absolute context size are scaled proportionally.
func CompactionConfigForModel(model string) CompactionConfig {
	cfg := DefaultCompactionConfig()
	cw := models.ContextWindow(model)
	if cw > 0 {
		cfg.ContextWindow = cw
		cfg.TokenBudget = int(float64(cw) * 0.90)
		// Scale leaf chunk size proportionally — 10% of context window,
		// clamped to [2_000, 20_000]. Without this, a 32K model would use
		// the same 20K chunk as a 200K model (62% of its window).
		leafChunk := cw / 10
		if leafChunk < 2_000 {
			leafChunk = 2_000
		}
		if leafChunk > 20_000 {
			leafChunk = 20_000
		}
		cfg.LeafChunkTokens = leafChunk
	}
	return cfg
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
	var p strings.Builder
	if aggressive {
		p.WriteString("You are a conversation compressor. Be extremely concise. ")
		p.WriteString("Only include durable facts: decisions made, files changed, errors encountered, and outcomes. ")
		p.WriteString("Omit all narrative, pleasantries, and exploratory discussion. ")
		p.WriteString("Use bullet points.\n\n")
	} else {
		p.WriteString("You are a conversation summarizer. ")
		p.WriteString("Your job is to create a clear, information-dense summary that preserves the important details.\n\n")
	}

	switch {
	case depth == 0:
		p.WriteString("Summarize the following conversation segment. ")
		p.WriteString("Preserve specific details: file names, commands run, decisions made, error messages, ")
		p.WriteString("configuration changes, and timestamps. ")
		p.WriteString("Write as a narrative that another AI agent could read to understand what happened.")
	case depth == 1:
		p.WriteString("Condense the following summaries into a higher-level overview. ")
		p.WriteString("Focus on decisions made, problems solved, and outcomes. ")
		p.WriteString("Omit repetitive details but keep file names and key technical specifics.")
	default:
		p.WriteString("Distill the following summaries to key decisions, outcomes, and unresolved items. ")
		p.WriteString("Be concise. Only preserve information that would be critical for understanding ")
		p.WriteString("the overall trajectory of this conversation.")
	}
	return p.String()
}

func buildLeafInput(msgs []platformdb.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] %s: %s\n\n",
			m.CreatedAt.Format("15:04:05"),
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

