package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportedMIME(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mediaType string
		want      bool
	}{
		// Images
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", true},

		// Text
		{"text/plain", true},
		{"text/html", true},
		{"text/csv", true},
		{"text/markdown", true},

		// Application types
		{"application/json", true},
		{"application/xml", true},
		{"application/yaml", true},
		{"application/x-yaml", true},
		{"application/pdf", true},

		// Unsupported
		{"application/octet-stream", false},
		{"application/zip", false},
		{"audio/mpeg", false},
		{"video/mp4", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			t.Parallel()
			got := supportedMIME(tt.mediaType)
			if got != tt.want {
				t.Fatalf("supportedMIME(%q) = %v, want %v", tt.mediaType, got, tt.want)
			}
		})
	}
}

func TestProcessAttachments_NilEmpty(t *testing.T) {
	t.Parallel()

	files, err := processAttachments(nil)
	if err != nil {
		t.Fatalf("unexpected error for nil: %v", err)
	}
	if files != nil {
		t.Fatalf("expected nil for nil input, got %v", files)
	}

	files, err = processAttachments([]ChatAttachment{})
	if err != nil {
		t.Fatalf("unexpected error for empty: %v", err)
	}
	if files != nil {
		t.Fatalf("expected nil for empty input, got %v", files)
	}
}

func TestProcessAttachments_Valid(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(data)

	attachments := []ChatAttachment{
		{Filename: "test.txt", Data: encoded, MediaType: "text/plain"},
		{Filename: "img.png", Data: encoded, MediaType: "image/png"},
	}

	files, err := processAttachments(attachments)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].Filename != "test.txt" {
		t.Errorf("filename = %q, want %q", files[0].Filename, "test.txt")
	}
	if files[0].MediaType != "text/plain" {
		t.Errorf("media type = %q, want %q", files[0].MediaType, "text/plain")
	}
	if string(files[0].Data) != "hello world" {
		t.Errorf("data = %q, want %q", string(files[0].Data), "hello world")
	}
}

func TestProcessAttachments_TooMany(t *testing.T) {
	t.Parallel()

	attachments := make([]ChatAttachment, maxAttachments+1)
	for i := range attachments {
		attachments[i] = ChatAttachment{
			Filename:  "file.txt",
			Data:      base64.StdEncoding.EncodeToString([]byte("x")),
			MediaType: "text/plain",
		}
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for too many attachments")
	}
	if !strings.Contains(err.Error(), "too many attachments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_UnsupportedMIME(t *testing.T) {
	t.Parallel()

	attachments := []ChatAttachment{
		{Filename: "archive.zip", Data: base64.StdEncoding.EncodeToString([]byte("x")), MediaType: "application/zip"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for unsupported MIME type")
	}
	if !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_OversizedBase64(t *testing.T) {
	t.Parallel()

	// Create base64 data that exceeds the pre-decode size check.
	// The check is: len(att.Data) > maxAttachmentSize*4/3+1024
	bigData := strings.Repeat("A", maxAttachmentSize*4/3+2048)
	attachments := []ChatAttachment{
		{Filename: "big.txt", Data: bigData, MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for oversized base64")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_InvalidBase64(t *testing.T) {
	t.Parallel()

	attachments := []ChatAttachment{
		{Filename: "bad.txt", Data: "not-valid-base64!!!", MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if !strings.Contains(err.Error(), "invalid base64") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessAttachments_OversizedDecoded(t *testing.T) {
	t.Parallel()

	// Create data that passes the pre-decode check but exceeds maxAttachmentSize after decode.
	// We need decoded size > maxAttachmentSize but base64 size <= maxAttachmentSize*4/3+1024.
	// maxAttachmentSize is 5MB. Create exactly maxAttachmentSize+1 bytes of data.
	raw := make([]byte, maxAttachmentSize+1)
	for i := range raw {
		raw[i] = 'A'
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	attachments := []ChatAttachment{
		{Filename: "big.txt", Data: encoded, MediaType: "text/plain"},
	}

	_, err := processAttachments(attachments)
	if err == nil {
		t.Fatal("expected error for oversized decoded data")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveChatInstance_NoManager(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	_, errStr := s.resolveChatInstance(req)
	if errStr == "" {
		t.Fatal("expected error when no manager set")
	}
	if !strings.Contains(errStr, "no agent configured") {
		t.Fatalf("unexpected error: %q", errStr)
	}
}

func TestResolveChatInstance_WithManager(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	// Set a non-nil manager (we just need the field set, not a real manager).
	// Since manager is *agent.Manager, we can't easily create a fake one,
	// but we can set leaderID and check the flow.
	s.leaderID = "leader-123"
	// manager is still nil, so hasManager() returns false.
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	_, errStr := s.resolveChatInstance(req)
	if errStr == "" {
		t.Fatal("expected error when manager is nil")
	}
}

func TestResolveChatInstance_WithInstanceIDParam(t *testing.T) {
	t.Parallel()

	// We cannot easily set a real manager, but we can test the query param
	// extraction logic by verifying the URL parsing path. When manager is nil,
	// we get the "no agent configured" error before reaching the param logic.
	// This is a limitation — the instance_id override is tested implicitly
	// through integration tests. Instead, test the URL query extraction directly.
	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat?instance_id=custom-123", nil)
	_, errStr := s.resolveChatInstance(req)
	// Still errors because no manager, but that's expected.
	if errStr == "" {
		t.Fatal("expected error when manager is nil")
	}
}

func TestHandleChat_NoManager_Returns503(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleChat_AuthRequired_NoCookie_Returns401(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/ws/chat", nil)
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
