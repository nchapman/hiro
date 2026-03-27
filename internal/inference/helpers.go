package inference

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

// EstimateTokens returns an approximate token count for a string.
// Uses the ~4 characters per token heuristic.
func EstimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && len(s) > 0 {
		return 1
	}
	return n
}

// EstimateFileTokens returns an approximate token count for file attachments.
// Uses ~1600 tokens per file as a rough average across images, PDFs, and text docs.
func EstimateFileTokens(count int) int {
	return count * 1600
}

// marshalMessage serializes a fantasy.Message to JSON for storage.
func marshalMessage(msg fantasy.Message) string {
	data, err := json.Marshal(msg)
	if err != nil {
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
	"read_file":      "Reading {{path}}",
	"write_file":     "Writing {{path}}",
	"edit_file":      "Editing {{file_path}}",
	"multiedit_file": "Editing {{file_path}}",
	"list_files":     "Listing {{path}}",
	"glob":           "Searching for {{pattern}}",
	"grep":           "Searching for {{pattern}}",
	"bash":           "Running command",
	"fetch":          "Fetching {{url}}",
	"job_output":     "Reading job output",
	"job_kill":       "Killing job",
	"spawn_session":  "Spawning {{agent}}",
	"resume_session": "Resuming session",
	"stop_session":   "Stopping session",
	"delete_session": "Deleting session",
	"list_sessions":  "Listing sessions",
	"send_message":   "Messaging session",
	"memory_read":    "Reading memory",
	"memory_write":   "Writing memory",
	"todos":          "Updating tasks",
	"history_search": "Searching history for {{query}}",
	"history_recall": "Recalling summary",
	"use_skill":      "Using skill {{name}}",
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
