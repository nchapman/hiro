package tools

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed fetch.md
var fetchDescription string

const (
	fetchTimeout    = 30 * time.Second
	maxResponseBody = 64000
)

type FetchParams struct {
	URL string `json:"url" description:"The URL to fetch."`
}

// NewFetchTool creates a tool that fetches content from URLs.
func NewFetchTool() fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		"fetch",
		fetchDescription,
		func(ctx context.Context, params FetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("url is required"), nil
			}

			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("url must start with http:// or https://"), nil
			}

			client := &http.Client{Timeout: fetchTimeout}
			req, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid request: %v", err)), nil
			}
			req.Header.Set("User-Agent", "Hive/1.0")

			resp, err := client.Do(req)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("fetch failed: %v", err)), nil
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error reading response: %v", err)), nil
			}

			truncated := ""
			if len(body) > maxResponseBody {
				body = body[:maxResponseBody]
				truncated = "\n[response truncated]"
			}

			result := fmt.Sprintf("HTTP %d %s\nContent-Type: %s\n\n%s%s",
				resp.StatusCode,
				resp.Status,
				resp.Header.Get("Content-Type"),
				string(body),
				truncated,
			)

			return fantasy.NewTextResponse(result), nil
		},
	)
}
