package inference

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
)

const (
	// charsPerToken is the approximate characters-per-token ratio used for
	// budget estimation throughout the inference package.
	charsPerToken = 4

	// imageTokensPerTenKB is the approximate token cost per 10 KB of image data.
	imageTokensPerTenKB = 170

	// minImageTokens is the floor token estimate for tiny images/thumbnails.
	minImageTokens = 85

	// maxImageTokens caps the token estimate for large images (resized down by providers).
	maxImageTokens = 1600

	// bytesPerTenKB is 10 KB in bytes, used as the divisor for image token estimation.
	bytesPerTenKB = 10240

	// pdfTokensPerPage is the approximate token cost per PDF page.
	pdfTokensPerPage = 1600

	// pdfBytesPerPage is the rough byte size per PDF page used for page count estimation.
	pdfBytesPerPage = 50 * 1024
)

// EstimateTokens returns an approximate token count for a string.
// Uses the ~4 characters per token heuristic.
func EstimateTokens(s string) int {
	n := len(s) / charsPerToken
	if n == 0 && s != "" {
		return 1
	}
	return n
}

// EstimateFileTokens returns an approximate token count for file attachments.
// Estimates vary by media type:
//   - Images: scaled by file size (~170 tokens per 10KB, clamped 85–1600)
//   - PDFs: ~1600 tokens per estimated page (~50KB/page)
//   - Text/code: raw byte length ÷ 4 (same heuristic as plain text)
//
// These are rough, provider-agnostic estimates for context budgeting.
func EstimateFileTokens(files []fantasy.FilePart) int {
	total := 0
	for _, f := range files {
		total += estimateOneFile(f)
	}
	return total
}

func estimateOneFile(f fantasy.FilePart) int {
	size := len(f.Data)
	if size == 0 {
		return 0
	}

	switch {
	case strings.HasPrefix(f.MediaType, "image/"):
		// Most providers resize images before tokenizing. Token cost
		// correlates loosely with file size. ~170 tokens per 10KB is a
		// reasonable middle ground across providers. Floor at 85 (tiny
		// thumbnails still cost something), cap at 1600 (large images
		// get resized down).
		tokens := size * imageTokensPerTenKB / bytesPerTenKB
		if tokens < minImageTokens {
			return minImageTokens
		}
		if tokens > maxImageTokens {
			return maxImageTokens
		}
		return tokens

	case f.MediaType == "application/pdf":
		// Rough estimate: ~50KB per page, ~1600 tokens per page.
		// Round up so we don't underestimate multi-page PDFs.
		pages := (size + pdfBytesPerPage - 1) / pdfBytesPerPage
		return pages * pdfTokensPerPage

	default:
		// Text, JSON, XML, code, etc. — treat as plain text.
		tokens := size / charsPerToken
		if tokens == 0 {
			return 1
		}
		return tokens
	}
}

// marshalMessage serializes a fantasy.Message to JSON for storage.
func marshalMessage(msg fantasy.Message) string {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("failed to marshal message", "role", msg.Role, "error", err)
		return "{}"
	}
	return string(data)
}

// extractText extracts text content from a message for search indexing.
func extractText(msg fantasy.Message) string {
	var parts []string
	for _, part := range msg.Content {
		if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
			parts = append(parts, tp.Text)
		}
		if tc, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
			parts = append(parts, fmt.Sprintf("[tool_call: %s]", tc.ToolName))
		}
		if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
			if text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](tr.Output); ok {
				parts = append(parts, text.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// extractToolResultOutput extracts the text output from a tool result.
func extractToolResultOutput(content fantasy.ToolResultOutputContent) (string, bool) {
	if content == nil {
		return "", false
	}
	if text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](content); ok {
		return text.Text, false
	}
	if errContent, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](content); ok {
		return errContent.Error.Error(), true
	}
	return "", false
}

// toolStatusMessages maps tool names to status message templates.
var toolStatusMessages = map[string]string{
	"Read":                     "Reading {{file_path}}",
	"Write":                    "Writing {{file_path}}",
	"Edit":                     "Editing {{file_path}}",
	"Glob":                     "Searching for {{pattern}}",
	"Grep":                     "Searching for {{pattern}}",
	"Bash":                     "Running command",
	"WebFetch":                 "Fetching {{url}}",
	"TaskOutput":               "Reading task output",
	"TaskStop":                 "Stopping task",
	"SpawnInstance":            "Spawning {{agent}}",
	"CreatePersistentInstance": "Creating {{agent}} instance",
	"ResumeInstance":           "Resuming instance",
	"StopInstance":             "Stopping instance",
	"DeleteInstance":           "Deleting instance",
	"ListInstances":            "Listing instances",
	"ListNodes":                "Listing nodes",
	"SendMessage":              "Messaging instance",
	"AddMemory":                "Saving memory",
	"ForgetMemory":             "Forgetting memory",
	"TodoWrite":                "Updating tasks",
	"HistorySearch":            "Searching history for {{query}}",
	"HistoryRecall":            "Recalling summary",
	"Skill":                    "Using skill {{name}}",
}

// resolveStatusMessage resolves a status template for a tool call.
func resolveStatusMessage(toolName, inputJSON string) string {
	tmpl, ok := toolStatusMessages[toolName]
	if !ok {
		return ""
	}
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	if inputJSON == "" {
		return ""
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &params); err != nil {
		return ""
	}

	result := tmpl
	for key, val := range params {
		placeholder := "{{" + key + "}}"
		if !strings.Contains(result, placeholder) {
			continue
		}
		s, ok := val.(string)
		if !ok {
			continue
		}
		result = strings.ReplaceAll(result, placeholder, s)
	}

	if strings.Contains(result, "{{") {
		return ""
	}
	return result
}

// InjectStatusMessages parses a fantasy raw_json message, finds tool-call
// entries, resolves their status, and returns the patched JSON.
func InjectStatusMessages(rawJSON string) string {
	var msg struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &msg); err != nil {
		return rawJSON
	}

	patched := false
	for _, part := range msg.Content {
		typ, _ := part["type"].(string)
		if typ != "tool-call" {
			continue
		}
		data, ok := part["data"].(map[string]any)
		if !ok {
			continue
		}
		toolName, _ := data["tool_name"].(string)
		input, _ := data["input"].(string)
		if toolName == "" {
			continue
		}
		status := resolveStatusMessage(toolName, input)
		if status != "" {
			data["status"] = status
			patched = true
		}
	}

	if !patched {
		return rawJSON
	}

	out, err := json.Marshal(msg)
	if err != nil {
		return rawJSON
	}
	return string(out)
}

const maxAgentResultSize = 32 * 1024

func truncateResult(s string) string {
	if len(s) <= maxAgentResultSize {
		return s
	}
	return s[:maxAgentResultSize] + "\n\n(result truncated)"
}
