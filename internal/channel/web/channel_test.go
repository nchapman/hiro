package web

import (
	"encoding/base64"
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
