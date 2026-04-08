package channel

import (
	"strings"

	"github.com/nchapman/hiro/internal/ipc"
)

const (
	eventTypeDelta  = "delta"
	eventTypeError  = "error"
	eventTypeSystem = "system"
	eventTypeClear  = "clear"
)

// clearConfirmation is the message sent to users when a session is cleared.
const clearConfirmation = "Session cleared."

// SplitMessage splits text into chunks of at most maxLen characters,
// preferring to split at newlines. Used by non-streaming channels
// (Telegram, Slack) to respect platform message length limits.
func SplitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for text != "" {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split at a newline within the limit.
		cut := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > 0 {
			cut = idx + 1 // include the newline
		}

		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

// FormatEvents extracts text content from inference events for delivery
// to non-streaming channels (Telegram, Slack). Only delta and error events
// produce output; tool calls, reasoning, and other event types are ignored.
func FormatEvents(events []ipc.ChatEvent) string {
	var buf strings.Builder
	for _, evt := range events {
		appendEvent(&buf, evt)
	}
	return buf.String()
}

// MakeBufferingOnEvent creates an OnEvent callback that buffers text deltas
// and error messages into a strings.Builder. Used by non-streaming channels
// to accumulate the full response before sending.
func MakeBufferingOnEvent(buf *strings.Builder) func(ipc.ChatEvent) error {
	return func(evt ipc.ChatEvent) error {
		appendEvent(buf, evt)
		return nil
	}
}

// appendEvent writes the content of a recognized event to the buffer.
// Handles delta (streaming text), system (slash command responses),
// clear (session reset confirmation), and error events. All other event
// types (tool_call, reasoning, etc.) are silently ignored.
func appendEvent(buf *strings.Builder, evt ipc.ChatEvent) {
	switch evt.Type {
	case eventTypeDelta:
		buf.WriteString(evt.Content)
	case eventTypeSystem:
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(evt.Content)
	case eventTypeClear:
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(clearConfirmation)
	case eventTypeError:
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString("Error: " + evt.Content)
	}
}
