package history

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// Summarizer generates summaries from text. Implementations call an LLM.
type Summarizer interface {
	// Summarize produces a summary of the input text.
	// systemPrompt sets the summarization style; input is the text to summarize.
	Summarize(ctx context.Context, systemPrompt, input string) (string, error)
}

// Compactor runs incremental compaction passes on conversation history.
type Compactor struct {
	store      *Store
	summarizer Summarizer
	config     Config
	logger     *slog.Logger
}

// Config controls compaction behavior.
type Config struct {
	TokenBudget          int     // max context window tokens (default: 180_000)
	FreshTailCount       int     // messages never compacted (default: 20)
	LeafChunkTokens      int     // max input tokens per leaf pass (default: 20_000)
	LeafTargetTokens     int     // target output tokens for leaf summaries (default: 1_200)
	CondenseTargetTokens int     // target output tokens for condensed summaries (default: 2_000)
	CompactThreshold     float64 // trigger full sweep when context > this fraction of budget (default: 0.75)
	LeafMinFanout        int     // minimum messages for a leaf pass (default: 8)
	CondenseMinFanout    int     // minimum summaries for condensation (default: 4)
}

// DefaultConfig returns reasonable default compaction settings.
func DefaultConfig() Config {
	return Config{
		TokenBudget:          180_000,
		FreshTailCount:       20,
		LeafChunkTokens:      20_000,
		LeafTargetTokens:     1_200,
		CondenseTargetTokens: 2_000,
		CompactThreshold:     0.75,
		LeafMinFanout:        8,
		CondenseMinFanout:    4,
	}
}

// NewCompactor creates a new compaction engine.
func NewCompactor(store *Store, summarizer Summarizer, cfg Config, logger *slog.Logger) *Compactor {
	return &Compactor{
		store:      store,
		summarizer: summarizer,
		config:     cfg,
		logger:     logger,
	}
}

// CompactIfNeeded runs incremental compaction if thresholds are met.
// This is the main entry point called after each conversation turn.
func (c *Compactor) CompactIfNeeded(ctx context.Context) error {
	// Check if there are enough message tokens outside the fresh tail for a leaf pass
	tokensOutside, err := c.store.MessageTokensOutsideTail(c.config.FreshTailCount)
	if err != nil {
		return fmt.Errorf("checking tokens outside tail: %w", err)
	}

	if tokensOutside >= c.config.LeafChunkTokens {
		if err := c.leafPass(ctx); err != nil {
			return fmt.Errorf("leaf pass: %w", err)
		}
	}

	// Check if total context exceeds threshold — if so, run a full sweep
	totalTokens, err := c.store.ContextTokenCount()
	if err != nil {
		return fmt.Errorf("checking context tokens: %w", err)
	}

	threshold := int(float64(c.config.TokenBudget) * c.config.CompactThreshold)
	if totalTokens > threshold {
		if err := c.fullSweep(ctx); err != nil {
			return fmt.Errorf("full sweep: %w", err)
		}
	}

	return nil
}

// leafPass compacts the oldest chunk of messages into a leaf summary (depth 0).
func (c *Compactor) leafPass(ctx context.Context) error {
	items, msgs, err := c.store.OldestMessageContextItems(c.config.FreshTailCount, c.config.LeafChunkTokens)
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

	// Three-level escalation: normal → aggressive → fallback
	summary, err := c.summarizeWithEscalation(ctx, 0, input, sourceTokens)
	if err != nil {
		return err
	}

	summaryID := generateSummaryID()
	summaryTokens := EstimateTokens(summary)

	sum := Summary{
		ID:           summaryID,
		Kind:         "leaf",
		Depth:        0,
		Content:      summary,
		Tokens:       summaryTokens,
		EarliestAt:   msgs[0].CreatedAt,
		LatestAt:     msgs[len(msgs)-1].CreatedAt,
		SourceTokens: sourceTokens,
	}

	if err := c.store.CreateSummary(sum); err != nil {
		return fmt.Errorf("creating summary: %w", err)
	}
	if err := c.store.LinkSummaryMessages(summaryID, msgIDs); err != nil {
		return fmt.Errorf("linking messages: %w", err)
	}
	if err := c.store.ReplaceContextItems(items[0].Ordinal, items[len(items)-1].Ordinal, summaryID); err != nil {
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

// condensationPass compacts same-depth summaries into a higher-level summary.
func (c *Compactor) condensationPass(ctx context.Context) (bool, error) {
	// Find the shallowest depth with enough contiguous summaries
	maxDepth, err := c.store.MaxSummaryDepth()
	if err != nil {
		return false, err
	}
	if maxDepth < 0 {
		return false, nil
	}

	for depth := 0; depth <= maxDepth; depth++ {
		items, sums, err := c.store.ContiguousSummariesAtDepth(depth, c.config.CondenseMinFanout)
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

		sum := Summary{
			ID:           summaryID,
			Kind:         "condensed",
			Depth:        depth + 1,
			Content:      summary,
			Tokens:       summaryTokens,
			EarliestAt:   sums[0].EarliestAt,
			LatestAt:     sums[len(sums)-1].LatestAt,
			SourceTokens: sourceTokens,
		}

		if err := c.store.CreateSummary(sum); err != nil {
			return false, fmt.Errorf("creating condensed summary: %w", err)
		}
		if err := c.store.LinkSummaryParents(summaryID, childIDs); err != nil {
			return false, fmt.Errorf("linking parents: %w", err)
		}
		if err := c.store.ReplaceContextItems(items[0].Ordinal, items[len(items)-1].Ordinal, summaryID); err != nil {
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

// fullSweep runs leaf and condensation passes until under budget or no progress.
func (c *Compactor) fullSweep(ctx context.Context) error {
	const maxIterations = 10
	for i := 0; i < maxIterations; i++ {
		// Try leaf passes first
		tokensOutside, err := c.store.MessageTokensOutsideTail(c.config.FreshTailCount)
		if err != nil {
			return err
		}
		if tokensOutside >= c.config.LeafChunkTokens {
			if err := c.leafPass(ctx); err != nil {
				return err
			}
			continue
		}

		// Then condensation passes
		condensed, err := c.condensationPass(ctx)
		if err != nil {
			return err
		}
		if condensed {
			continue
		}

		// No progress — done
		break
	}
	return nil
}

// summarizeWithEscalation tries normal → aggressive → fallback truncation.
func (c *Compactor) summarizeWithEscalation(ctx context.Context, depth int, input string, sourceTokens int) (string, error) {
	// Determine target tokens based on depth
	targetTokens := c.config.LeafTargetTokens
	if depth > 0 {
		targetTokens = c.config.CondenseTargetTokens
	}

	// Try normal summarization
	prompt := summarizationPrompt(depth, false)
	summary, err := c.summarizer.Summarize(ctx, prompt, input)
	if err != nil {
		return "", fmt.Errorf("normal summarization: %w", err)
	}

	summaryTokens := EstimateTokens(summary)
	if summaryTokens <= targetTokens {
		return summary, nil
	}

	// Escalate to aggressive
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

	// Final fallback: deterministic truncation
	c.logger.Warn("falling back to deterministic truncation",
		"summary_tokens", summaryTokens,
		"target_tokens", targetTokens,
	)
	return fallbackTruncate(summary, targetTokens), nil
}

// generateSummaryID creates a unique ID for a summary.
func generateSummaryID() string {
	return "sum_" + uuid.New().String()[:8]
}
