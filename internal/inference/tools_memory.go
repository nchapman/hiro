package inference

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

//go:embed add_memory.md
var addMemoryDescription string

//go:embed forget_memory.md
var forgetMemoryDescription string

// maxMemoryEntries is the upper limit on memory entries. When exceeded, the
// oldest entries (top of the file) are evicted to make room.
const maxMemoryEntries = 100

func buildMemoryTools(instanceDir string, mu *sync.Mutex) []Tool {
	return wrapAll([]fantasy.AgentTool{
		fantasy.NewAgentTool("AddMemory",
			addMemoryDescription,
			func(ctx context.Context, input struct {
				Content string `json:"content" description:"The memory to save. A single concise line — no newlines."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if mu != nil {
					mu.Lock()
					defer mu.Unlock()
				}
				return handleAddMemory(instanceDir, input.Content), nil
			},
		),

		fantasy.NewAgentTool("ForgetMemory",
			forgetMemoryDescription,
			func(ctx context.Context, input struct {
				Match string `json:"match" description:"Substring to match against existing memories (case-insensitive)."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if mu != nil {
					mu.Lock()
					defer mu.Unlock()
				}
				return handleForgetMemory(instanceDir, input.Match), nil
			},
		),
	})
}

func handleAddMemory(instanceDir, rawContent string) fantasy.ToolResponse {
	content := strings.TrimSpace(rawContent)
	if content == "" {
		return fantasy.NewTextErrorResponse("content cannot be empty")
	}
	// Strip any newlines — one memory, one line.
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.ReplaceAll(content, "\r", " ")

	existing, err := config.ReadMemoryFile(instanceDir)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read memory: %v", err))
	}

	entries := parseMemoryEntries(existing)

	// Append new entry with date stamp.
	date := time.Now().Format("2006-01-02")
	entry := fmt.Sprintf("- %s [%s]", content, date)
	entries = append(entries, entry)

	// Evict oldest entries if over limit.
	if len(entries) > maxMemoryEntries {
		entries = entries[len(entries)-maxMemoryEntries:]
	}

	if err := config.WriteMemoryFile(instanceDir, strings.Join(entries, "\n")+"\n"); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write memory: %v", err))
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Memory saved. %d/%d entries.", len(entries), maxMemoryEntries))
}

func handleForgetMemory(instanceDir, rawMatch string) fantasy.ToolResponse {
	match := strings.TrimSpace(rawMatch)
	if match == "" {
		return fantasy.NewTextErrorResponse("match cannot be empty")
	}

	existing, err := config.ReadMemoryFile(instanceDir)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read memory: %v", err))
	}

	entries := parseMemoryEntries(existing)
	if len(entries) == 0 {
		return fantasy.NewTextResponse("No memories to forget.")
	}

	matchLower := strings.ToLower(match)
	var kept, removed []string
	for _, e := range entries {
		if strings.Contains(strings.ToLower(entryContent(e)), matchLower) {
			removed = append(removed, e)
		} else {
			kept = append(kept, e)
		}
	}

	if len(removed) == 0 {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("no memories matched %q", match))
	}

	content := ""
	if len(kept) > 0 {
		content = strings.Join(kept, "\n") + "\n"
	}

	if err := config.WriteMemoryFile(instanceDir, content); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write memory: %v", err))
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Forgot %d memory(s). %d remaining.", len(removed), len(kept)))
}

// entryContent strips the trailing " [YYYY-MM-DD]" date stamp from a memory
// entry so that ForgetMemory matches against content only, not dates.
func entryContent(entry string) string {
	if i := strings.LastIndex(entry, " ["); i >= 0 {
		return entry[:i]
	}
	return entry
}

// parseMemoryEntries splits memory.md content into non-empty lines.
func parseMemoryEntries(content string) []string {
	var entries []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}
