package inference

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// maxHistorySearchResults is the maximum number of results returned by history search queries.
const maxHistorySearchResults = 20

//go:embed history_search.md
var historySearchDescription string

//go:embed history_recall.md
var historyRecallDescription string

func buildHistoryTools(pdb *platformdb.DB, sessionID string) []fantasy.AgentTool {
	searchHandler := func(ctx context.Context, input struct {
		Query string `json:"query" description:"Search query (full-text search)."`
		Scope string `json:"scope" description:"Where to search: 'messages', 'summaries', or 'all'. Default: 'all'." default:"all"`
	}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
		if input.Query == "" {
			return fantasy.NewTextErrorResponse("query is required"), nil
		}
		scope := input.Scope
		if scope == "" {
			scope = "all"
		}

		var results []platformdb.SearchResult
		var err error
		switch scope {
		case "messages":
			results, err = pdb.SearchMessages(ctx, sessionID, input.Query, maxHistorySearchResults)
		case "summaries":
			results, err = pdb.SearchSummaries(ctx, sessionID, input.Query, maxHistorySearchResults)
		default:
			results, err = pdb.Search(ctx, sessionID, input.Query, maxHistorySearchResults)
		}
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("search failed: %v", err)), nil
		}
		if len(results) == 0 {
			return fantasy.NewTextResponse("No results found."), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Found %d results:\n\n", len(results))
		for _, r := range results {
			fmt.Fprintf(&sb, "- [%s:%s] %s\n", r.Type, r.ID, r.Snippet)
		}
		return fantasy.NewTextResponse(sb.String()), nil
	}

	return []fantasy.AgentTool{
		fantasy.NewAgentTool("HistorySearch", historySearchDescription, searchHandler),
		fantasy.NewAgentTool("HistoryRecall",
			historyRecallDescription,
			func(ctx context.Context, input struct {
				SummaryID string `json:"summary_id" description:"The ID of a summary to expand (e.g. 'sum_abc123')."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleHistoryRecall(ctx, pdb, input.SummaryID)
			},
		),
	}
}

func handleHistoryRecall(ctx context.Context, pdb *platformdb.DB, summaryID string) (fantasy.ToolResponse, error) {
	if summaryID == "" {
		return fantasy.NewTextErrorResponse("summary_id is required"), nil
	}

	sum, err := pdb.GetSummary(ctx, summaryID)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("summary not found: %v", err)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Summary %s (depth %d, %s)\n\n", sum.ID, sum.Depth, sum.Kind)
	fmt.Fprintf(&sb, "Time range: %s to %s\n",
		sum.EarliestAt.Format("2006-01-02 15:04"),
		sum.LatestAt.Format("2006-01-02 15:04"))
	fmt.Fprintf(&sb, "Compression: %d tokens → %d tokens\n\n", sum.SourceTokens, sum.Tokens)
	sb.WriteString(sum.Content)

	if sum.Kind == "leaf" {
		if err := appendSourceMessages(ctx, pdb, sum.ID, &sb); err != nil {
			return fantasy.NewTextErrorResponse(err.Error()), nil
		}
	} else {
		if err := appendChildSummaries(ctx, pdb, sum.ID, &sb); err != nil {
			return fantasy.NewTextErrorResponse(err.Error()), nil
		}
	}

	return fantasy.NewTextResponse(truncateResult(sb.String())), nil
}

func appendSourceMessages(ctx context.Context, pdb *platformdb.DB, summaryID string, sb *strings.Builder) error {
	msgIDs, err := pdb.GetSummarySourceMessages(ctx, summaryID)
	if err != nil {
		return fmt.Errorf("failed to load source messages: %w", err)
	}
	if len(msgIDs) == 0 {
		return nil
	}
	msgs, err := pdb.GetMessages(ctx, msgIDs)
	if err != nil {
		return fmt.Errorf("failed to load messages: %w", err)
	}
	sb.WriteString("\n\n---\n### Source Messages\n\n")
	for _, m := range msgs {
		fmt.Fprintf(sb, "[%s] **%s**: %s\n\n",
			m.CreatedAt.Format("15:04:05"), m.Role,
			truncateResult(m.Content))
	}
	return nil
}

func appendChildSummaries(ctx context.Context, pdb *platformdb.DB, summaryID string, sb *strings.Builder) error {
	childIDs, err := pdb.GetSummaryChildren(ctx, summaryID)
	if err != nil {
		return fmt.Errorf("failed to load child summaries: %w", err)
	}
	if len(childIDs) == 0 {
		return nil
	}
	sb.WriteString("\n\n---\n### Child Summaries\n\n")
	for _, cid := range childIDs {
		child, err := pdb.GetSummary(ctx, cid)
		if err != nil {
			continue
		}
		fmt.Fprintf(sb, "**%s** (depth %d, %s to %s):\n%s\n\n",
			child.ID, child.Depth,
			child.EarliestAt.Format("2006-01-02 15:04"),
			child.LatestAt.Format("2006-01-02 15:04"),
			child.Content)
	}
	return nil
}
