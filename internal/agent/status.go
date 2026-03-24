package agent

import (
	"encoding/json"
	"strings"
)

// toolStatusMessages maps tool names to status message templates.
// Templates use {{param}} placeholders that are resolved from the
// tool's JSON input at emit time, so updating a template here
// immediately affects all future events.
var toolStatusMessages = map[string]string{
	// Built-in tools
	"read_file":   "Reading {{path}}",
	"write_file":  "Writing {{path}}",
	"edit":        "Editing {{file_path}}",
	"multiedit":   "Editing {{file_path}}",
	"list_files":  "Listing {{path}}",
	"glob":        "Searching for {{pattern}}",
	"grep":        "Searching for {{pattern}}",
	"bash":        "Running command",
	"fetch":       "Fetching {{url}}",
	"job_output":  "Reading job output",
	"job_kill":    "Killing job",

	// Spawn tool
	"spawn_session": "Spawning {{agent}}",

	// Coordinator tools
	"create_session": "Creating session for {{agent}}",
	"start_session":  "Starting session",
	"stop_session":   "Stopping session",
	"delete_session": "Deleting session",
	"list_sessions":  "Listing sessions",
	"send_message":   "Messaging session",

	// Persistent agent tools
	"memory_read":    "Reading memory",
	"memory_write":   "Writing memory",
	"todos":          "Updating tasks",
	"history_search": "Searching history for {{query}}",
	"history_recall": "Recalling summary",

	// Skill tool
	"use_skill": "Using skill {{name}}",
}

// injectStatusMessages parses a fantasy raw_json message, finds tool-call
// entries, resolves their status from the current templates, and returns
// the patched JSON. If the message has no tool calls or can't be parsed,
// the original string is returned unchanged.
func injectStatusMessages(rawJSON string) string {
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

// resolveStatusMessage resolves a status template for a tool call,
// replacing {{param}} placeholders with values from the JSON input.
// Returns empty string if no template is registered for the tool.
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

	// If any placeholders remain unresolved, return empty so the
	// frontend falls back to the raw tool name.
	if strings.Contains(result, "{{") {
		return ""
	}

	return result
}
