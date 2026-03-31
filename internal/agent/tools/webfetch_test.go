package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetch_SSRFBlocked(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(true)

	// Start a local server — it'll be on 127.0.0.1 which is loopback.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should not reach here"))
	}))
	defer ts.Close()

	// Loopback should be blocked even when the server is actually running.
	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if !isErr {
		t.Fatalf("expected error for loopback URL, got: %s", content)
	}
	if !strings.Contains(content, "blocked") && !strings.Contains(content, "non-public") {
		t.Errorf("expected SSRF block message, got: %s", content)
	}
}

func TestFetch_SSRFBlocksNonRoutable(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(true)

	// Test that the transport itself rejects private IPs by checking
	// the ssrfTransport directly with a known-connectable loopback addr.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	// With SSRF enabled, even localhost test servers are blocked.
	client := &http.Client{Transport: ssrfTransport}
	_, err := client.Get(ts.URL)
	if err == nil {
		t.Fatal("expected SSRF transport to block localhost")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("expected non-public error, got: %v", err)
	}
}

func TestFetch_SSRFAllowedWhenDisabled(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	// With SSRF disabled, localhost test servers work fine.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if isErr {
		t.Fatalf("unexpected error with SSRF disabled: %s", content)
	}
}

func TestFetch_BasicGet(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello from server"))
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "200") {
		t.Errorf("expected 200 status, got %q", content)
	}
	if !strings.Contains(content, "hello from server") {
		t.Errorf("expected response body, got %q", content)
	}
	if !strings.Contains(content, "text/plain") {
		t.Errorf("expected content type, got %q", content)
	}
}

func TestFetch_JSON(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key": "value"}`))
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, `"key"`) {
		t.Errorf("expected JSON body, got %q", content)
	}
}

func TestFetch_404(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "404") {
		t.Errorf("expected 404 status, got %q", content)
	}
}

func TestFetch_EmptyURL(t *testing.T) {
	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": ""}`)
	if !isErr {
		t.Fatal("expected error for empty URL")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestFetch_InvalidURL(t *testing.T) {
	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "not-a-url"}`)
	if !isErr {
		t.Fatal("expected error for invalid URL")
	}
	if !strings.Contains(content, "http") {
		t.Errorf("expected URL scheme error, got %q", content)
	}
}

func TestFetch_UnreachableHost(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	// Use a closed server to fail fast instead of timing out
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close() // immediately close so connection is refused

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if !isErr {
		t.Fatal("expected error for unreachable host")
	}
	if !strings.Contains(content, "fetch failed") {
		t.Errorf("expected 'fetch failed' error, got %q", content)
	}
}

func TestFetch_LargeResponse(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	// Return more than maxResponseBody bytes
	big := strings.Repeat("x", maxResponseBody+1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	content, isErr := runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "truncated") {
		t.Errorf("expected truncation notice, got length %d", len(content))
	}
}

func TestFetch_ChecksUserAgent(t *testing.T) {
	origEnabled := ssrfEnabled.Load()
	defer ssrfEnabled.Store(origEnabled)
	SetSSRFProtection(false)

	var gotUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	tool := NewWebFetchTool()
	runTool(t, tool, `{"url": "`+ts.URL+`"}`)
	if gotUA != "Hive/1.0" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "Hive/1.0")
	}
}
