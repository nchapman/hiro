package inference

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hivebot/internal/platform/db"
)

func buildHistoryTools(pdb *platformdb.DB, sessionID string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("history_search",
			"Search your conversation history for past messages and summaries.",
			func(ctx context.Context, input struct {
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
					results, err = pdb.SearchMessages(sessionID, input.Query, 20)
				case "summaries":
					results, err = pdb.SearchSummaries(sessionID, input.Query, 20)
				default:
					results, err = pdb.Search(sessionID, input.Query, 20)
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
			},
		),
		fantasy.NewAgentTool("history_recall",
			"Expand a conversation summary to see its full content and the lower-level summaries or messages it was created from.",
			func(ctx context.Context, input struct {
				SummaryID string `json:"summary_id" description:"The ID of a summary to expand (e.g. 'sum_abc123')."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.SummaryID == "" {
					return fantasy.NewTextErrorResponse("summary_id is required"), nil
				}

				sum, err := pdb.GetSummary(input.SummaryID)
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
					msgIDs, err := pdb.GetSummarySourceMessages(sum.ID)
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to load source messages: %v", err)), nil
					}
					if len(msgIDs) > 0 {
						msgs, err := pdb.GetMessages(msgIDs)
						if err != nil {
							return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to load messages: %v", err)), nil
						}
						sb.WriteString("\n\n---\n### Source Messages\n\n")
						for _, m := range msgs {
							fmt.Fprintf(&sb, "[%s] **%s**: %s\n\n",
								m.CreatedAt.Format("15:04:05"), m.Role,
								truncateResult(m.Content))
						}
					}
				} else {
					childIDs, err := pdb.GetSummaryChildren(sum.ID)
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to load child summaries: %v", err)), nil
					}
					if len(childIDs) > 0 {
						sb.WriteString("\n\n---\n### Child Summaries\n\n")
						for _, cid := range childIDs {
							child, err := pdb.GetSummary(cid)
							if err != nil {
								continue
							}
							fmt.Fprintf(&sb, "**%s** (depth %d, %s to %s):\n%s\n\n",
								child.ID, child.Depth,
								child.EarliestAt.Format("2006-01-02 15:04"),
								child.LatestAt.Format("2006-01-02 15:04"),
								child.Content)
						}
					}
				}

				return fantasy.NewTextResponse(truncateResult(sb.String())), nil
			},
		),
	}
}
