package inference

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/google/uuid"

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
type CompactionConfig struct {
	TokenBudget          int
	FreshTailCount       int
	LeafChunkTokens      int
	LeafTargetTokens     int
	CondenseTargetTokens int
	CompactThreshold     float64
	LeafMinFanout        int
	CondenseMinFanout    int
}

// DefaultCompactionConfig returns reasonable defaults.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
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

// CompactIfNeeded runs incremental compaction if thresholds are met.
func (c *Compactor) CompactIfNeeded(ctx context.Context) error {
	tokensOutside, err := c.pdb.MessageTokensOutsideTail(c.sessionID, c.config.FreshTailCount)
	if err != nil {
		return fmt.Errorf("checking tokens outside tail: %w", err)
	}

	if tokensOutside >= c.config.LeafChunkTokens {
		if err := c.leafPass(ctx); err != nil {
			return fmt.Errorf("leaf pass: %w", err)
		}
	}

	totalTokens, err := c.pdb.ContextTokenCount(c.sessionID)
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
	return "sum_" + uuid.New().String()[:8]
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

// NewCompactorForSession is unused but keeps the function signature consistent.
var _ = time.Now // suppress unused import if needed
