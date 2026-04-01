package tools

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
)

//go:embed webfetch.md
var webFetchDescription string

type WebFetchParams struct {
	URL string `json:"url" description:"The URL to fetch."`
}

// ssrfTransport is an http.Transport that blocks connections to private,
// loopback, and link-local addresses. DNS is resolved before dialing and
// all resolved IPs are checked, preventing DNS rebinding attacks where a
// name initially resolves to a public IP then switches to a private one.
var ssrfTransport = &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("fetch blocked: invalid address %s: %w", addr, err)
		}

		// Resolve DNS explicitly so we can check all IPs before connecting.
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("fetch blocked: DNS resolution failed for %s: %w", host, err)
		}

		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
				return nil, fmt.Errorf("fetch blocked: %s resolves to non-public address %s", host, a)
			}
		}

		// Dial the first resolved address directly to avoid re-resolving.
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0], port))
	},
}

// ssrfEnabled controls whether SSRF protection is active. Defaults to
// true (secure by default). Tests that need localhost access disable it
// explicitly via SetSSRFProtection(false). Uses atomic.Bool for
// goroutine safety since the fetch tool runs concurrently.
var ssrfEnabled atomic.Bool

func init() { ssrfEnabled.Store(true) }

// SetSSRFProtection enables or disables SSRF protection for the fetch tool.
func SetSSRFProtection(enabled bool) {
	ssrfEnabled.Store(enabled)
}

// NewWebFetchTool creates a tool that fetches content from URLs.
// When SSRF protection is enabled, blocks requests to private/loopback/link-local addresses.
func NewWebFetchTool() fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		"WebFetch",
		webFetchDescription,
		func(ctx context.Context, params WebFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("url is required"), nil
			}

			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("url must start with http:// or https://"), nil
			}

			client := &http.Client{Timeout: fetchTimeout}
			if ssrfEnabled.Load() {
				client.Transport = ssrfTransport
			}
			req, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid request: %v", err)), nil
			}
			req.Header.Set("User-Agent", "Hiro/1.0")

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
