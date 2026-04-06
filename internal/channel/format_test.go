package channel

import (
	"strings"
	"testing"

	"github.com/nchapman/hiro/internal/ipc"
)

func TestFormatEvents_DeltasOnly(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "hello "},
		{Type: "delta", Content: "world"},
	}
	text := FormatEvents(events)
	if text != "hello world" {
		t.Errorf("got %q, want %q", text, "hello world")
	}
}

func TestFormatEvents_WithError(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "delta", Content: "partial"},
		{Type: "error", Content: "something broke"},
	}
	text := FormatEvents(events)
	if !strings.Contains(text, "partial") || !strings.Contains(text, "Error: something broke") {
		t.Errorf("got %q", text)
	}
	// Error after content should have separator.
	if !strings.Contains(text, "\n\n") {
		t.Error("expected separator before error")
	}
}

func TestFormatEvents_ErrorOnly(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "error", Content: "fail"},
	}
	text := FormatEvents(events)
	if text != "Error: fail" {
		t.Errorf("got %q, want %q", text, "Error: fail")
	}
}

func TestFormatEvents_IgnoresOtherTypes(t *testing.T) {
	t.Parallel()

	events := []ipc.ChatEvent{
		{Type: "tool_call", ToolName: "Bash"},
		{Type: "reasoning_delta", Content: "thinking"},
		{Type: "tool_result", Output: "output"},
	}
	text := FormatEvents(events)
	if text != "" {
		t.Errorf("got %q, want empty", text)
	}
}

func TestFormatEvents_Empty(t *testing.T) {
	t.Parallel()

	text := FormatEvents(nil)
	if text != "" {
		t.Errorf("got %q, want empty", text)
	}
}

func TestMakeBufferingOnEvent_Deltas(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := MakeBufferingOnEvent(&buf)

	_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "hello "})
	_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "world"})

	if buf.String() != "hello world" {
		t.Errorf("got %q", buf.String())
	}
}

func TestMakeBufferingOnEvent_ErrorOnEmptyBuffer(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := MakeBufferingOnEvent(&buf)

	// Error on empty buffer — no separator.
	_ = onEvent(ipc.ChatEvent{Type: "error", Content: "fail"})
	if buf.String() != "Error: fail" {
		t.Errorf("got %q", buf.String())
	}
}

func TestMakeBufferingOnEvent_ErrorAfterContent(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := MakeBufferingOnEvent(&buf)

	_ = onEvent(ipc.ChatEvent{Type: "delta", Content: "partial"})
	_ = onEvent(ipc.ChatEvent{Type: "error", Content: "oops"})

	if !strings.Contains(buf.String(), "\n\nError: oops") {
		t.Errorf("got %q, expected separator", buf.String())
	}
}

func TestMakeBufferingOnEvent_IgnoresOtherTypes(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := MakeBufferingOnEvent(&buf)

	_ = onEvent(ipc.ChatEvent{Type: "tool_call"})
	_ = onEvent(ipc.ChatEvent{Type: "reasoning_delta"})

	if buf.String() != "" {
		t.Errorf("got %q, want empty", buf.String())
	}
}

func TestMakeBufferingOnEvent_ReturnsNil(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	onEvent := MakeBufferingOnEvent(&buf)

	err := onEvent(ipc.ChatEvent{Type: "delta", Content: "x"})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}
