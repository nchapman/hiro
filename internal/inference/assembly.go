package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// maxTailBudgetPercent is the maximum percentage of the token budget that the
// fresh tail (most recent messages) may consume during context assembly.
const maxTailBudgetPercent = 80

// percentDivisor is the denominator for percentage calculations.
const percentDivisor = 100

// AssembleResult holds the assembled context for an LLM call.
type AssembleResult struct {
	Messages        []fantasy.Message
	EstimatedTokens int
}

// Assemble builds a []fantasy.Message from the context items in the platform DB,
// respecting the token budget.
func Assemble(ctx context.Context, pdb *platformdb.DB, sessionID string, cfg CompactionConfig) (AssembleResult, error) {
	items, err := pdb.GetContextItems(ctx, sessionID)
	if err != nil {
		return AssembleResult{}, fmt.Errorf("loading context items: %w", err)
	}
	if len(items) == 0 {
		return AssembleResult{}, nil
	}

	all, err := resolveAllItems(ctx, pdb, items)
	if err != nil {
		return AssembleResult{}, err
	}

	// Split into evictable prefix and protected fresh tail.
	tailStart := max(0, len(all)-cfg.FreshTailCount)
	freshTail := all[tailStart:]
	evictable := all[:tailStart]

	freshTail, evictable, tailTokens := capFreshTail(freshTail, evictable, all, cfg)

	// Fill remaining budget from evictable, newest first.
	kept := fillBudget(evictable, cfg.TokenBudget-tailTokens)

	return assembleMessages(kept, freshTail), nil
}

type resolvedItem struct {
	msg    fantasy.Message
	tokens int
}

func resolveAllItems(ctx context.Context, pdb *platformdb.DB, items []platformdb.ContextItem) ([]resolvedItem, error) {
	all := make([]resolvedItem, len(items))
	for i, item := range items {
		msg, tokens, err := resolveItem(ctx, pdb, item)
		if err != nil {
			return nil, fmt.Errorf("resolving item %d: %w", item.Ordinal, err)
		}
		all[i] = resolvedItem{msg: msg, tokens: tokens}
	}
	return all, nil
}

// capFreshTail ensures the fresh tail doesn't exceed 80% of the token budget.
// Returns the (possibly truncated) tail, updated evictable slice, and tail token count.
func capFreshTail(freshTail, evictable, all []resolvedItem, cfg CompactionConfig) ([]resolvedItem, []resolvedItem, int) {
	tailTokens := 0
	for _, r := range freshTail {
		tailTokens += r.tokens
	}

	maxTailTokens := cfg.TokenBudget * maxTailBudgetPercent / percentDivisor
	if tailTokens > maxTailTokens {
		originalCount := len(freshTail)
		for len(freshTail) > 1 && tailTokens > maxTailTokens {
			tailTokens -= freshTail[0].tokens
			freshTail = freshTail[1:]
		}
		evictable = all[:len(all)-len(freshTail)]
		slog.Warn("fresh tail truncated to fit token budget",
			"original_count", originalCount,
			"kept_count", len(freshTail),
			"tail_tokens", tailTokens,
			"max_tail_tokens", maxTailTokens,
		)
	}
	return freshTail, evictable, tailTokens
}

// fillBudget selects items from evictable (newest first) that fit within the
// remaining token budget, returned in chronological order.
func fillBudget(evictable []resolvedItem, remaining int) []resolvedItem {
	var keptReverse []resolvedItem
	for i := len(evictable) - 1; i >= 0; i-- {
		if remaining <= 0 || evictable[i].tokens > remaining {
			break
		}
		keptReverse = append(keptReverse, evictable[i])
		remaining -= evictable[i].tokens
	}
	// Reverse to restore chronological order.
	kept := make([]resolvedItem, len(keptReverse))
	for i, r := range keptReverse {
		kept[len(keptReverse)-1-i] = r
	}
	return kept
}

func assembleMessages(kept, freshTail []resolvedItem) AssembleResult {
	messages := make([]fantasy.Message, 0, len(kept)+len(freshTail))
	totalTokens := 0
	for _, r := range kept {
		messages = append(messages, r.msg)
		totalTokens += r.tokens
	}
	for _, r := range freshTail {
		messages = append(messages, r.msg)
		totalTokens += r.tokens
	}
	return AssembleResult{
		Messages:        messages,
		EstimatedTokens: totalTokens,
	}
}

// resolveItem converts a context item to a fantasy.Message.
func resolveItem(ctx context.Context, pdb *platformdb.DB, item platformdb.ContextItem) (fantasy.Message, int, error) {
	switch item.ItemType {
	case platformdb.ItemTypeMessage:
		if item.MessageID == nil {
			return fantasy.Message{}, 0, fmt.Errorf("message item has nil message_id")
		}
		msg, err := pdb.GetMessage(ctx, *item.MessageID)
		if err != nil {
			return fantasy.Message{}, 0, err
		}

		var fMsg fantasy.Message
		if err := json.Unmarshal([]byte(msg.RawJSON), &fMsg); err == nil && len(fMsg.Content) > 0 {
			return fMsg, msg.Tokens, nil
		}

		role := fantasy.MessageRole(msg.Role)
		return fantasy.Message{
			Role:    role,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: msg.Content}},
		}, msg.Tokens, nil

	case platformdb.ItemTypeSummary:
		if item.SummaryID == nil {
			return fantasy.Message{}, 0, fmt.Errorf("summary item has nil summary_id")
		}
		sum, err := pdb.GetSummary(ctx, *item.SummaryID)
		if err != nil {
			return fantasy.Message{}, 0, err
		}

		text := fmt.Sprintf(
			"<conversation_summary id=%q depth=\"%d\" time_range=\"%s to %s\">\n%s\n</conversation_summary>",
			sum.ID, sum.Depth,
			sum.EarliestAt.Format("2006-01-02 15:04"),
			sum.LatestAt.Format("2006-01-02 15:04"),
			sum.Content,
		)
		return fantasy.NewUserMessage(text), sum.Tokens, nil

	default:
		return fantasy.Message{}, 0, fmt.Errorf("unknown item type: %q", item.ItemType)
	}
}
