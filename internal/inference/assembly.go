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

	type resolved struct {
		msg    fantasy.Message
		tokens int
	}
	all := make([]resolved, len(items))
	for i, item := range items {
		msg, tokens, err := resolveItem(ctx, pdb, item)
		if err != nil {
			return AssembleResult{}, fmt.Errorf("resolving item %d: %w", item.Ordinal, err)
		}
		all[i] = resolved{msg: msg, tokens: tokens}
	}

	// Split into evictable prefix and protected fresh tail.
	tailStart := len(all) - cfg.FreshTailCount
	if tailStart < 0 {
		tailStart = 0
	}
	freshTail := all[tailStart:]
	evictable := all[:tailStart]

	tailTokens := 0
	for _, r := range freshTail {
		tailTokens += r.tokens
	}

	// Cap fresh tail to prevent budget overflow. Reserve at most 80% of the
	// token budget for the tail; shrink from the oldest end if exceeded.
	maxTailTokens := cfg.TokenBudget * maxTailBudgetPercent / percentDivisor
	if tailTokens > maxTailTokens {
		originalCount := len(freshTail)
		for len(freshTail) > 1 && tailTokens > maxTailTokens {
			tailTokens -= freshTail[0].tokens
			freshTail = freshTail[1:]
		}
		tailStart = len(all) - len(freshTail)
		evictable = all[:tailStart]
		slog.Warn("fresh tail truncated to fit token budget",
			"original_count", originalCount,
			"kept_count", len(freshTail),
			"tail_tokens", tailTokens,
			"max_tail_tokens", maxTailTokens,
		)
	}

	// Fill remaining budget from evictable, newest first.
	remaining := cfg.TokenBudget - tailTokens
	var keptReverse []resolved
	for i := len(evictable) - 1; i >= 0; i-- {
		if remaining <= 0 || evictable[i].tokens > remaining {
			break
		}
		keptReverse = append(keptReverse, evictable[i])
		remaining -= evictable[i].tokens
	}
	// Reverse to restore chronological order.
	kept := make([]resolved, len(keptReverse))
	for i, r := range keptReverse {
		kept[len(keptReverse)-1-i] = r
	}

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
	}, nil
}

// resolveItem converts a context item to a fantasy.Message.
func resolveItem(ctx context.Context, pdb *platformdb.DB, item platformdb.ContextItem) (fantasy.Message, int, error) {
	switch item.ItemType {
	case "message":
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

	case "summary":
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
