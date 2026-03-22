package history

import (
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
)

// AssembleResult holds the assembled context for an LLM call.
type AssembleResult struct {
	Messages        []fantasy.Message
	EstimatedTokens int
}

// Assemble builds a []fantasy.Message from the context items in the store,
// respecting the token budget. The fresh tail (most recent messages) is
// always included. Older items are included newest-first until the budget
// is exhausted.
func Assemble(store *Store, cfg Config) (AssembleResult, error) {
	items, err := store.GetContextItems()
	if err != nil {
		return AssembleResult{}, fmt.Errorf("loading context items: %w", err)
	}
	if len(items) == 0 {
		return AssembleResult{}, nil
	}

	// Resolve all items to messages + token costs
	type resolved struct {
		msg    fantasy.Message
		tokens int
	}
	all := make([]resolved, len(items))
	for i, item := range items {
		msg, tokens, err := resolveItem(store, item)
		if err != nil {
			return AssembleResult{}, fmt.Errorf("resolving item %d: %w", item.Ordinal, err)
		}
		all[i] = resolved{msg: msg, tokens: tokens}
	}

	// Split into evictable prefix and protected fresh tail
	tailStart := len(all) - cfg.FreshTailCount
	if tailStart < 0 {
		tailStart = 0
	}

	freshTail := all[tailStart:]
	evictable := all[:tailStart]

	// Fresh tail is always included
	tailTokens := 0
	for _, r := range freshTail {
		tailTokens += r.tokens
	}

	// Fill remaining budget from evictable, newest first.
	// Stop at the first item that doesn't fit to keep context contiguous.
	remaining := cfg.TokenBudget - tailTokens
	var kept []resolved
	for i := len(evictable) - 1; i >= 0; i-- {
		if remaining <= 0 || evictable[i].tokens > remaining {
			break
		}
		kept = append([]resolved{evictable[i]}, kept...)
		remaining -= evictable[i].tokens
	}

	// Assemble final message list
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
func resolveItem(store *Store, item ContextItem) (fantasy.Message, int, error) {
	switch item.ItemType {
	case "message":
		if item.MessageID == nil {
			return fantasy.Message{}, 0, fmt.Errorf("message item has nil message_id")
		}
		msg, err := store.GetMessage(*item.MessageID)
		if err != nil {
			return fantasy.Message{}, 0, err
		}

		// Try to reconstruct from raw JSON first
		var fMsg fantasy.Message
		if err := json.Unmarshal([]byte(msg.RawJSON), &fMsg); err == nil && len(fMsg.Content) > 0 {
			return fMsg, msg.Tokens, nil
		}

		// Fallback: reconstruct from role + content
		role := fantasy.MessageRole(msg.Role)
		return fantasy.Message{
			Role:    role,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: msg.Content}},
		}, msg.Tokens, nil

	case "summary":
		if item.SummaryID == nil {
			return fantasy.Message{}, 0, fmt.Errorf("summary item has nil summary_id")
		}
		sum, err := store.GetSummary(*item.SummaryID)
		if err != nil {
			return fantasy.Message{}, 0, err
		}

		// Wrap summary as a user message with XML tags for context
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
