package inference

import (
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"
)

// textPartText extracts the text from a fantasy.TextPart with a checked type assertion.
func textPartText(t *testing.T, part fantasy.MessagePart) string {
	t.Helper()
	tp, ok := part.(fantasy.TextPart)
	if !ok {
		t.Fatalf("expected fantasy.TextPart, got %T", part)
	}
	return tp.Text
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},           // len=2, 2/4=0 but >0 → 1
		{"abc", 1},          // len=3, 3/4=0 but >0 → 1
		{"abcd", 1},         // len=4, 4/4=1
		{"hello world!", 3}, // len=12, 12/4=3
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEstimateFileTokens(t *testing.T) {
	makeFile := func(mediaType string, size int) fantasy.FilePart {
		return fantasy.FilePart{
			Filename:  "test",
			Data:      make([]byte, size),
			MediaType: mediaType,
		}
	}

	t.Run("empty", func(t *testing.T) {
		if got := EstimateFileTokens(nil); got != 0 {
			t.Errorf("nil files = %d, want 0", got)
		}
	})

	t.Run("zero size file", func(t *testing.T) {
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("image/png", 0)})
		if got != 0 {
			t.Errorf("zero size = %d, want 0", got)
		}
	})

	t.Run("small image hits floor", func(t *testing.T) {
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("image/jpeg", 100)})
		if got != 85 {
			t.Errorf("tiny image = %d, want 85", got)
		}
	})

	t.Run("medium image scales", func(t *testing.T) {
		// 50*1024 * 170 / 10240 = 850
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("image/png", 50*1024)})
		if got != 850 {
			t.Errorf("50KB image = %d, want 850", got)
		}
	})

	t.Run("large image hits cap", func(t *testing.T) {
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("image/webp", 500*1024)})
		if got != 1600 {
			t.Errorf("500KB image = %d, want 1600", got)
		}
	})

	t.Run("pdf single page", func(t *testing.T) {
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("application/pdf", 30*1024)})
		if got != 1600 {
			t.Errorf("small PDF = %d, want 1600 (1 page min)", got)
		}
	})

	t.Run("pdf multi page", func(t *testing.T) {
		// 200KB / 50KB = 4 pages → 6400 tokens
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("application/pdf", 200*1024)})
		if got != 6400 {
			t.Errorf("200KB PDF = %d, want 6400", got)
		}
	})

	t.Run("pdf rounds up", func(t *testing.T) {
		// 99KB → rounds up to 2 pages → 3200 tokens
		got := EstimateFileTokens([]fantasy.FilePart{makeFile("application/pdf", 99*1024)})
		if got != 3200 {
			t.Errorf("99KB PDF = %d, want 3200", got)
		}
	})

	t.Run("text file uses char heuristic", func(t *testing.T) {
		data := []byte(strings.Repeat("abcd", 100)) // 400 bytes → 100 tokens
		files := []fantasy.FilePart{{Filename: "test.txt", Data: data, MediaType: "text/plain"}}
		got := EstimateFileTokens(files)
		if got != 100 {
			t.Errorf("400 byte text = %d, want 100", got)
		}
	})

	t.Run("multiple files sum", func(t *testing.T) {
		files := []fantasy.FilePart{
			makeFile("image/jpeg", 100),       // 85 (floor)
			makeFile("text/plain", 400),       // 100
			makeFile("application/pdf", 1024), // 1600 (1 page min)
		}
		got := EstimateFileTokens(files)
		want := 85 + 100 + 1600
		if got != want {
			t.Errorf("mixed files = %d, want %d", got, want)
		}
	})
}

func TestResolveStatusMessage(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{
			name: "simple template",
			tool: "Read", input: `{"file_path":"main.go"}`,
			want: "Reading main.go",
		},
		{
			name: "no template params",
			tool: "Bash", input: `{"command":"ls"}`,
			want: "Running command",
		},
		{
			name: "unknown tool",
			tool: "unknown_tool", input: `{}`,
			want: "",
		},
		{
			name: "missing required param",
			tool: "Read", input: `{}`,
			want: "", // unresolved placeholder → empty
		},
		{
			name: "empty input JSON",
			tool: "Read", input: "",
			want: "",
		},
		{
			name: "glob with pattern",
			tool: "Glob", input: `{"pattern":"**/*.go"}`,
			want: "Searching for **/*.go",
		},
		{
			name: "non-string param value",
			tool: "Read", input: `{"file_path": 42}`,
			want: "", // placeholder not substituted → empty
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStatusMessage(tt.tool, tt.input)
			if got != tt.want {
				t.Errorf("resolveStatusMessage(%q, %q) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateResult(t *testing.T) {
	short := "hello"
	if got := truncateResult(short); got != short {
		t.Errorf("short string should not be truncated")
	}

	long := make([]byte, maxAgentResultSize+100)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateResult(string(long))
	if len(got) > maxAgentResultSize+50 {
		t.Errorf("truncated result too long: %d", len(got))
	}
	if got[len(got)-len("(result truncated)"):] != "(result truncated)" {
		t.Error("expected truncation marker")
	}
}

func TestInjectStatusMessages_NoToolCalls(t *testing.T) {
	raw := `{"role":"user","content":[{"type":"text","data":{"text":"hello"}}]}`
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should not modify messages without tool calls")
	}
}

func TestInjectStatusMessages_InvalidJSON(t *testing.T) {
	raw := "not json"
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should return original on invalid JSON")
	}
}

func TestInjectStatusMessages_WithToolCall(t *testing.T) {
	raw := `{"role":"assistant","content":[{"type":"tool-call","data":{"tool_name":"Read","input":"{\"file_path\":\"main.go\"}"}}]}`
	got := InjectStatusMessages(raw)
	if got == raw {
		t.Error("should have modified message with tool call")
	}
	if !strings.Contains(got, "Reading main.go") {
		t.Errorf("expected status message injected, got: %s", got)
	}
}

func TestInjectStatusMessages_ToolCallWithoutTemplate(t *testing.T) {
	raw := `{"role":"assistant","content":[{"type":"tool-call","data":{"tool_name":"UnknownTool","input":"{}"}}]}`
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should not modify when tool has no status template")
	}
}

func TestInjectStatusMessages_EmptyToolName(t *testing.T) {
	raw := `{"role":"assistant","content":[{"type":"tool-call","data":{"tool_name":"","input":"{}"}}]}`
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should not modify when tool name is empty")
	}
}

func TestMarshalMessage(t *testing.T) {
	msg := fantasy.NewUserMessage("hello world")
	result := marshalMessage(msg)
	if result == "{}" {
		t.Error("expected non-empty JSON")
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected message content in JSON, got: %s", result)
	}
	if !strings.Contains(result, "user") {
		t.Errorf("expected role in JSON, got: %s", result)
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		msg  fantasy.Message
		want string
	}{
		{
			name: "text message",
			msg:  fantasy.NewUserMessage("hello world"),
			want: "hello world",
		},
		{
			name: "empty message",
			msg:  fantasy.Message{Role: "user"},
			want: "",
		},
		{
			name: "tool call part",
			msg: fantasy.Message{
				Role: "assistant",
				Content: []fantasy.MessagePart{
					fantasy.ToolCallPart{ToolName: "Read"},
				},
			},
			want: "[tool_call: Read]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.msg)
			if got != tt.want {
				t.Errorf("extractText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToolResultOutput(t *testing.T) {
	t.Run("nil content", func(t *testing.T) {
		text, isErr := extractToolResultOutput(nil)
		if text != "" || isErr {
			t.Errorf("nil content: got (%q, %v), want (\"\", false)", text, isErr)
		}
	})

	t.Run("text content", func(t *testing.T) {
		content := fantasy.ToolResultOutputContentText{Text: "file contents here"}
		text, isErr := extractToolResultOutput(content)
		if text != "file contents here" || isErr {
			t.Errorf("text content: got (%q, %v)", text, isErr)
		}
	})

	t.Run("error content", func(t *testing.T) {
		content := fantasy.ToolResultOutputContentError{Error: errors.New("not found")}
		text, isErr := extractToolResultOutput(content)
		if text != "not found" || !isErr {
			t.Errorf("error content: got (%q, %v), want (\"not found\", true)", text, isErr)
		}
	})
}
