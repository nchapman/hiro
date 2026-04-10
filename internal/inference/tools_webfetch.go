package inference

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed webfetch.md
var webFetchDescription string

const (
	webFetchTimeout = 30 * time.Second
	webDialTimeout  = 10 * time.Second
	maxResponseBody = 64000
	maxRedirects    = 10
)

type webFetchParams struct {
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

		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("fetch blocked: DNS resolution failed for %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("fetch blocked: DNS returned no addresses for %s", host)
		}

		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip != nil && isBlockedIP(ip) {
				return nil, fmt.Errorf("fetch blocked: %s resolves to non-public address %s", host, a)
			}
		}

		dialer := &net.Dialer{Timeout: webDialTimeout}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0], port))
	},
}

// isBlockedIP returns true for loopback, private, link-local, and multicast addresses.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified()
}

func buildWebFetchTool() Tool {
	return wrap(fantasy.NewParallelAgentTool(
		"WebFetch",
		webFetchDescription,
		func(ctx context.Context, params webFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return executeWebFetch(ctx, params)
		},
	))
}

func executeWebFetch(ctx context.Context, params webFetchParams) (fantasy.ToolResponse, error) {
	if params.URL == "" {
		return fantasy.NewTextErrorResponse("url is required"), nil
	}

	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return fantasy.NewTextErrorResponse("url must start with http:// or https://"), nil
	}

	client := &http.Client{
		Timeout:       webFetchTimeout,
		Transport:     ssrfTransport,
		CheckRedirect: ssrfRedirectCheck(ctx),
	}
	req, err := http.NewRequestWithContext(ctx, "GET", params.URL, http.NoBody)
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

	result := fmt.Sprintf("HTTP %s\nContent-Type: %s\n\n%s%s",
		resp.Status,
		resp.Header.Get("Content-Type"),
		string(body),
		truncated,
	)

	return fantasy.NewTextResponse(result), nil
}

// ssrfRedirectCheck returns an http.Client CheckRedirect function that
// validates redirect targets against the SSRF IP blocklist.
func ssrfRedirectCheck(ctx context.Context) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		host := req.URL.Hostname()
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return fmt.Errorf("fetch blocked: DNS resolution failed for redirect target %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return fmt.Errorf("fetch blocked: DNS returned no addresses for redirect target %s", host)
		}
		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip != nil && isBlockedIP(ip) {
				return fmt.Errorf("fetch blocked: redirect to %s resolves to non-public address %s", host, a)
			}
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
}
