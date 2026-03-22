package history

import (
	"fmt"
	"strings"
)

// summarizationPrompt returns the system prompt for summarizing conversation
// content at the given depth. Deeper summaries are more abstract.
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

// buildLeafInput formats messages for leaf summarization.
func buildLeafInput(msgs []Message) string {
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

// buildCondensationInput formats summaries for condensation.
func buildCondensationInput(sums []Summary) string {
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

// fallbackTruncate deterministically truncates content as a last resort
// when LLM summarization produces bloated output.
func fallbackTruncate(content string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n[Truncated for context management]"
}
