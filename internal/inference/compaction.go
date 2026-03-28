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

	// Look for a summary immediately preceding this chunk for dedup context.
	prevContext := c.precedingSummaryContent(items[0].Ordinal, nil)

	input := buildLeafInput(msgs)
	sourceTokens := 0
	var msgIDs []int64
	for _, m := range msgs {
		sourceTokens += m.Tokens
		msgIDs = append(msgIDs, m.ID)
	}

	summary, err := c.summarizeWithEscalation(ctx, 0, input, sourceTokens, prevContext)
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

	// Fetch all context items once for dedup lookups across depth iterations.
	allContextItems, err := c.pdb.GetContextItems(c.sessionID)
	if err != nil {
		return false, err
	}

	for depth := 0; depth <= maxDepth; depth++ {
		items, sums, err := c.pdb.ContiguousSummariesAtDepth(c.sessionID, depth, c.config.CondenseMinFanout)
		if err != nil {
			return false, err
		}
		if len(sums) == 0 {
			continue
		}

		// Look for a summary immediately preceding this run for dedup context.
		prevContext := c.precedingSummaryContent(items[0].Ordinal, allContextItems)

		input := buildCondensationInput(sums)
		sourceTokens := 0
		var childIDs []string
		for _, s := range sums {
			sourceTokens += s.Tokens
			childIDs = append(childIDs, s.ID)
		}

		summary, err := c.summarizeWithEscalation(ctx, depth+1, input, sourceTokens, prevContext)
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

func (c *Compactor) summarizeWithEscalation(ctx context.Context, depth int, input string, sourceTokens int, prevContext string) (string, error) {
	targetTokens := c.config.LeafTargetTokens
	if depth > 0 {
		targetTokens = c.config.CondenseTargetTokens
	}

	prompt := summarizationPrompt(depth, false, targetTokens, prevContext)
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
	prompt = summarizationPrompt(depth, true, targetTokens, prevContext)
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

// precedingSummaryContent finds the nearest summary context item before
// startOrdinal and returns its content. Returns "" if none exists or on
// any error. startOrdinal must be the ordinal of an item currently in the
// context_items list (callers pass items[0].Ordinal from a just-fetched slice).
//
// When allItems is provided, it reuses that slice instead of re-fetching from
// the DB. This avoids a redundant full scan in the condensation path where
// ContiguousSummariesAtDepth already fetches all context items.
func (c *Compactor) precedingSummaryContent(startOrdinal int, allItems []platformdb.ContextItem) string {
	if allItems == nil {
		var err error
		allItems, err = c.pdb.GetContextItems(c.sessionID)
		if err != nil {
			return ""
		}
	}

	// Find the context item at startOrdinal, then walk backwards for a summary.
	var prevSummaryID *string
	for i, ci := range allItems {
		if ci.Ordinal >= startOrdinal {
			for j := i - 1; j >= 0; j-- {
				if allItems[j].ItemType == "summary" && allItems[j].SummaryID != nil {
					prevSummaryID = allItems[j].SummaryID
					break
				}
			}
			break
		}
	}
	if prevSummaryID == nil {
		return ""
	}

	sum, err := c.pdb.GetSummary(*prevSummaryID)
	if err != nil {
		return ""
	}
	return sum.Content
}

// --- Prompt/formatting helpers ---

func summarizationPrompt(depth int, aggressive bool, targetTokens int, prevContext string) string {
	var base string
	switch {
	case depth == 0 && !aggressive:
		base = leafPrompt
	case depth == 0 && aggressive:
		base = leafAggressivePrompt
	case depth == 1 && !aggressive:
		base = condenseD1Prompt
	case depth == 1 && aggressive:
		base = condenseD1AggressivePrompt
	case depth == 2 && !aggressive:
		base = condenseD2Prompt
	case depth == 2 && aggressive:
		base = condenseD2AggressivePrompt
	case !aggressive:
		base = condenseD3PlusPrompt
	default:
		base = condenseD3PlusAggressivePrompt
	}

	result := base

	// Dedup constraint — "what to do" instruction, before budget guidance.
	if prevContext != "" {
		result += "\n\nThe following summary immediately precedes this chunk — do not repeat or restate facts already covered there, even if phrased differently:\n<previous_context>\n" + prevContext + "\n</previous_context>"
	}

	result += fmt.Sprintf("\n\nBudget: ~%d tokens (~%d characters). Prioritize the most important facts if you cannot fit everything.", targetTokens, targetTokens*4)

	// Expand footer last — terminal instruction the model sees before generating.
	result += expandFooter

	return result
}

// expandFooter is appended to all summarization prompts. It tells the model
// to list what was dropped so the agent can use history_recall to expand later.
const expandFooter = ` End with: "Expand for details about: <comma-separated list of topics you compressed or dropped>".`

// leafPrompt summarizes raw conversation messages into bullet points.
const leafPrompt = `Objective: Preserve maximum fidelity in minimum words. The original messages will be deleted — your summary is the only record. Someone will later use it to answer questions about this conversation. Named entities — people, places, titles, event names — are especially high-value and must be preserved.

Keep: facts, decisions (what AND why), opinions, feelings, motivations (the "why" behind actions), dates, names, numbers, identifiers, personal details. Attribute who said/did/felt what. Copy all named entities, paths, commands, error messages, and numbers verbatim. When a fact contains a list of items or names, preserve ALL elements. Every event must have a date — "when" questions are very common.

Discard: greetings, filler, pleasantries, and anything already established in an earlier bullet.

Format: one bullet per fact, concise. Convert relative dates using the message timestamps ("yesterday" on 2024-03-15 → 2024-03-14). When a relative date resolves to a specific day, use the resolved date without approximation. Reserve ~ only for genuinely vague references ("recently", "a while ago"). Match the precision of the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. State a person's identity/role once, then use their name only in subsequent bullets. No prose or framing sentences.`

// leafAggressivePrompt is the escalation when normal leaf output exceeds
// the token target. Same objective, much tighter budget.
const leafAggressivePrompt = `Objective: Preserve maximum fidelity in minimum words. The original messages will be deleted — your summary is the only record. You are on a very tight space budget. Named entities — people, places, titles, event names ��� must be preserved even under tight budgets.

Keep: facts, decisions (what AND why), opinions, feelings, motivations, dates, names, numbers, identifiers, personal details. Attribute each fact to its speaker. Copy all named entities, paths, commands, error messages, and numbers verbatim. Preserve ALL elements when a fact is a list. Every event must have a date.

Discard: greetings, filler, restated information, context already established in earlier bullets.

Format: one fact per bullet, ~15 words max. When a relative date resolves to a specific day, use it without ~. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. State a person's identity/role once; use name only after. No prose.`

// --- Depth-aware condensation prompts ---
//
// Each depth level progressively deprioritizes operational detail and
// emphasizes durable decisions and outcomes.

// condenseD1Prompt merges leaf summaries into a session-level summary.
const condenseD1Prompt = `Objective: Merge these conversation summaries into one record with maximum fidelity in minimum words. The inputs are from consecutive segments. Your output replaces all of them. Named entities — people, places, titles, event names — must survive merging.

Keep: every distinct fact from any input. Merge related facts about the same topic or person into single bullets where possible. Preserve the date for every event — never merge away a date. Preserve motivations (why someone did something). When a fact is a list of names or items, preserve ALL elements. Copy identifiers, error messages, paths, commands, and numbers verbatim — paraphrasing them destroys searchability.

Discard: strict duplicates, redundant phrasing, repeated context (e.g. a person's role restated across inputs — keep it once).

Format: bullet points grouped by person or topic. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. No prose or framing sentences.`

// condenseD1AggressivePrompt is the tight-budget version of D1 condensation.
const condenseD1AggressivePrompt = `Objective: Merge these conversation summaries with maximum fidelity in minimum words. Your output replaces all inputs — anything you drop becomes permanently unanswerable. Very tight space budget. Named entities must survive.

Keep: every distinct fact. Merge related facts aggressively. Preserve the date for every event. Preserve ALL list elements (names, items, events). Copy identifiers, error messages, paths, and commands verbatim.

Discard: duplicates, redundant phrasing, repeated context.

Format: terse bullet points. Match date precision to the source: YYYY-MM-DD for exact days, Month YYYY for month-level, YYYY for year-level. Don't fabricate precision. No prose.`

// condenseD2Prompt consolidates session-level summaries into phase-level.
// At this depth, operational detail matters less — focus on trajectory.
const condenseD2Prompt = `Objective: Consolidate these session-level summaries into a higher-level record. A future reader should understand trajectory, outcomes, and durable constraints — not per-session process steps. Preserve named entities (people, places, titles).

Keep: decisions still in effect and their rationale. Decisions that evolved — what changed and why. Completed work with outcomes. Active constraints, limitations, and known issues. Current state of in-progress work. Dates for every event. Copy identifiers, error messages, paths, and commands verbatim — paraphrasing them destroys searchability.

Discard: transient session-local mechanics (not active constraints), process scaffolding, intermediate states superseded by later outcomes, identifiers no longer relevant.

Format: bullet points grouped by topic. Include a timeline of key milestones with dates. Match date precision to the source. Don't fabricate precision. No prose or framing sentences.`

// condenseD2AggressivePrompt is the tight-budget version of D2 condensation.
const condenseD2AggressivePrompt = `Objective: Consolidate session-level summaries — trajectory, outcomes, and durable constraints only. Very tight space budget. Anything you drop becomes permanently unanswerable. Preserve named entities.

Keep: decisions in effect + rationale, evolved decisions, completed outcomes, active constraints, dates for every event. Copy identifiers and commands verbatim.

Discard: transient session-local mechanics, process scaffolding, superseded intermediate states.

Format: terse bullet points grouped by topic. Include a short timeline of key milestones with dates. No prose.`

// condenseD3PlusPrompt creates long-term memory from phase-level summaries.
// Only durable context survives — this may persist for the rest of the conversation.
const condenseD3PlusPrompt = `Objective: Create a high-level memory from multiple phase-level summaries. This may persist for the rest of the conversation. Keep only durable context.

Keep: key decisions and rationale. What was accomplished and current state. Active constraints and hard limitations. Important relationships between people, systems, or concepts. Lessons learned — recurring problems, failed approaches, and explicit conclusions about what to avoid. Dates for major milestones. Copy identifiers and commands verbatim unless no longer relevant.

Discard: operational and process detail. Method details unless the method itself was the decision. Specific references unless essential for continuation.

Format: concise bullet points. Include a brief timeline with dates for major milestones. No prose or framing sentences.`

// condenseD3PlusAggressivePrompt is the tight-budget version of D3+ condensation.
const condenseD3PlusAggressivePrompt = `Objective: High-level memory from phase summaries — durable context only. Very tight space budget.

Keep: key decisions + rationale, accomplishments, active constraints, important relationships, milestone dates. Copy identifiers verbatim unless no longer relevant.

Discard: operational detail, method specifics, non-essential references.

Format: terse bullet points. Include a short timeline of major milestones with dates. No prose.`

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

