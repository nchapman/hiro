// Package agent wraps charmbracelet/fantasy to provide the Hive agent runtime.
// An agent loads its identity and tools from markdown config, then runs an
// agentic loop managed by the AgentManager.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"

	"github.com/nchapman/hivebot/internal/agent/tools"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
)

// ProviderType identifies which LLM provider to use.
type ProviderType string

const (
	ProviderAnthropic  ProviderType = "anthropic"
	ProviderOpenRouter ProviderType = "openrouter"
)

// Agent is a Hive agent backed by a fantasy agent loop.
type Agent struct {
	config         config.AgentConfig
	agent          fantasy.Agent
	workingDir     string
	instanceDir    string // for re-reading memory.md, identity.md at runtime
	agentDefDir    string // agent definition directory (for re-scanning skills)
	sharedSkillDir string // workspace-level shared skills directory
	bgMgr          *tools.BackgroundJobManager
	logger         *slog.Logger
}

// Options configures how an agent connects to an LLM provider.
type Options struct {
	Provider       ProviderType
	APIKey         string
	Model          string                // overrides the model from agent config
	WorkingDir     string                // working directory for file/bash tools
	ExtraTools     []fantasy.AgentTool   // additional tools injected by the manager
	Identity       string                // instance identity (from identity.md)
	InstanceDir    string                // instance directory for runtime file access (memory.md etc.)
	AgentDefDir    string                // agent definition directory (for runtime skill re-scan)
	SharedSkillDir string                // workspace-level shared skills directory
	LM             fantasy.LanguageModel // if set, bypasses provider creation (for testing)
}

// New creates a new Hive agent from the given config.
func New(ctx context.Context, cfg config.AgentConfig, opts Options, logger *slog.Logger) (*Agent, error) {
	var lm fantasy.LanguageModel
	if opts.LM != nil {
		lm = opts.LM
	} else {
		model := cfg.Model
		if opts.Model != "" {
			model = opts.Model
		}
		if model == "" {
			return nil, fmt.Errorf("no model specified for agent %q", cfg.Name)
		}
		var err error
		lm, err = createLanguageModel(ctx, opts, model)
		if err != nil {
			return nil, fmt.Errorf("creating language model: %w", err)
		}
	}

	workingDir := opts.WorkingDir
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determining working directory: %w", err)
		}
	}

	a := &Agent{
		config:         cfg,
		workingDir:     workingDir,
		instanceDir:    opts.InstanceDir,
		agentDefDir:    opts.AgentDefDir,
		sharedSkillDir: opts.SharedSkillDir,
		bgMgr:          tools.NewBackgroundJobManager(),
		logger:         logger,
	}

	agentTools := a.buildTools()
	agentTools = append(agentTools, opts.ExtraTools...)

	// Add use_skill tool if this agent can have skills
	if a.agentDefDir != "" || len(cfg.Skills) > 0 {
		agentTools = append(agentTools, buildSkillTool(&a.config))
	}

	// Build the initial system prompt. This is also rebuilt dynamically
	// on each StreamChat call via PrepareStep to pick up memory changes.
	systemPrompt := a.currentSystemPrompt()

	a.agent = fantasy.NewAgent(lm,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
	)

	return a, nil
}

// Name returns the agent's configured name.
func (a *Agent) Name() string {
	return a.config.Name
}

// Config returns the agent's configuration.
func (a *Agent) Config() config.AgentConfig {
	return a.config
}

// Conversation holds the message history for a multi-turn chat session.
// When backed by a history engine, messages are persisted and compacted.
type Conversation struct {
	Messages []fantasy.Message
	engine   *history.Engine // non-nil for persistent agents with history
}

// NewConversation creates a new ephemeral conversation (no persistence).
func NewConversation() *Conversation {
	return &Conversation{}
}

// NewConversationWithHistory creates a conversation backed by a history engine.
// Messages are persisted to SQLite and automatically compacted.
func NewConversationWithHistory(engine *history.Engine) *Conversation {
	return &Conversation{engine: engine}
}

// StreamChat sends a message in the context of a conversation, streaming
// the response token-by-token. The conversation history is automatically
// updated with both the user message and the assistant's response.
// If onDelta returns an error, streaming stops.
//
// When the conversation has a history engine, messages are persisted and
// context is assembled from the DB within the token budget.
func (a *Agent) StreamChat(ctx context.Context, conv *Conversation, prompt string, onDelta func(text string) error) (string, error) {
	var messages []fantasy.Message

	if conv.engine != nil {
		// Assemble existing context from DB within token budget.
		// The user message is NOT persisted yet — we only persist after
		// a successful stream to keep history consistent.
		assembled, err := conv.engine.Assemble()
		if err != nil {
			a.logger.Warn("failed to assemble context, falling back to empty", "error", err)
		}
		messages = assembled.Messages
	} else {
		messages = conv.Messages
	}

	result, err := a.agent.Stream(ctx, fantasy.AgentStreamCall{
		Prompt:   prompt,
		Messages: messages,
		PrepareStep: func(ctx context.Context, opts fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
			if opts.StepNumber == 0 {
				sp := a.currentSystemPrompt()
				return ctx, fantasy.PrepareStepResult{System: &sp}, nil
			}
			return ctx, fantasy.PrepareStepResult{}, nil
		},
		OnTextDelta: func(id, text string) error {
			if onDelta != nil {
				return onDelta(text)
			}
			return nil
		},
	})
	if err != nil {
		return "", fmt.Errorf("agent stream: %w", err)
	}

	if conv.engine != nil {
		// Persist user message + assistant response atomically after success
		rawJSON := marshalMessage(fantasy.NewUserMessage(prompt))
		if err := conv.engine.Ingest("user", prompt, rawJSON); err != nil {
			a.logger.Warn("failed to ingest user message", "error", err)
		}
		for _, step := range result.Steps {
			for _, msg := range step.Messages {
				rawJSON := marshalMessage(msg)
				text := extractText(msg)
				role := string(msg.Role)
				if err := conv.engine.Ingest(role, text, rawJSON); err != nil {
					a.logger.Warn("failed to ingest step message", "role", role, "error", err)
				}
			}
		}

		// Trigger incremental compaction
		if err := conv.engine.Compact(ctx); err != nil {
			a.logger.Warn("compaction failed", "error", err)
		}
	} else {
		// Ephemeral: keep messages in memory
		conv.Messages = append(conv.Messages, fantasy.NewUserMessage(prompt))
		for _, step := range result.Steps {
			conv.Messages = append(conv.Messages, step.Messages...)
		}
	}

	return result.Response.Content.Text(), nil
}

// marshalMessage serializes a fantasy.Message to JSON for storage.
func marshalMessage(msg fantasy.Message) string {
	data, err := json.Marshal(msg)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// extractText extracts all text content from a message for search indexing.
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

// currentSystemPrompt rebuilds the system prompt from config and disk.
// Called at the start of each StreamChat turn (PrepareStep step 0) to pick up
// changes from memory_write or identity edits. Within-turn writes are visible
// via tool results, not via the system prompt.
func (a *Agent) currentSystemPrompt() string {
	identity := ""
	memory := ""
	todos := ""
	if a.instanceDir != "" {
		if id, err := config.ReadOptionalFile(filepath.Join(a.instanceDir, "identity.md")); err != nil {
			a.logger.Warn("could not read identity.md", "error", err)
		} else {
			identity = id
		}
		if mem, err := config.ReadMemoryFile(a.instanceDir); err != nil {
			a.logger.Warn("could not read memory.md", "error", err)
		} else {
			memory = mem
		}
		if t, err := config.ReadTodos(a.instanceDir); err != nil {
			a.logger.Warn("could not read todos.yaml", "error", err)
		} else {
			todos = config.FormatTodos(t)
		}
	}

	// Re-scan skills from disk each turn (skills may be added at runtime).
	if a.agentDefDir != "" {
		agentSkills, err := config.LoadSkills(filepath.Join(a.agentDefDir, "skills"))
		if err != nil {
			a.logger.Warn("could not reload skills", "error", err)
		} else {
			sharedSkills, sharedErr := config.LoadSkills(a.sharedSkillDir)
			if sharedErr != nil {
				a.logger.Warn("could not reload shared skills", "error", sharedErr)
			}
			a.config.Skills = config.MergeSkills(agentSkills, sharedSkills)
		}
	}

	return buildSystemPrompt(a.config, identity, memory, todos)
}

// buildSystemPrompt assembles the system prompt from the agent's config
// and dynamic content. Order: soul → identity → memories → todos → instructions → tools → skills.
func buildSystemPrompt(cfg config.AgentConfig, identity, memory, todos string) string {
	var p strings.Builder

	if cfg.Soul != "" {
		p.WriteString(cfg.Soul)
		p.WriteString("\n\n")
	}

	if identity != "" {
		p.WriteString("## Identity\n\n")
		p.WriteString(identity)
		p.WriteString("\n\n")
	}

	if memory != "" {
		p.WriteString("## Memories\n\n")
		p.WriteString(memory)
		p.WriteString("\n\n")
	}

	if todos != "" {
		p.WriteString("## Current Tasks\n\n")
		p.WriteString(todos)
		p.WriteString("\n\n")
	}

	p.WriteString(cfg.Prompt)

	if cfg.Tools != "" {
		p.WriteString("\n\n## Tool Notes\n\n")
		p.WriteString(cfg.Tools)
	}

	if len(cfg.Skills) > 0 {
		p.WriteString("\n\n## Skills\n\n")
		p.WriteString("IMPORTANT: Always call use_skill before performing a task that matches a skill. " +
			"Skills contain critical instructions and formats — do not attempt the task without activating the skill first.\n\n")
		for _, skill := range cfg.Skills {
			fmt.Fprintf(&p, "- **%s**: %s\n", skill.Name, skill.Description)
		}
	}

	return strings.TrimSpace(p.String())
}

func createLanguageModel(ctx context.Context, opts Options, model string) (fantasy.LanguageModel, error) {
	switch opts.Provider {
	case ProviderAnthropic:
		p, err := anthropic.New(anthropic.WithAPIKey(opts.APIKey))
		if err != nil {
			return nil, err
		}
		return p.LanguageModel(ctx, model)

	case ProviderOpenRouter:
		p, err := openrouter.New(openrouter.WithAPIKey(opts.APIKey))
		if err != nil {
			return nil, err
		}
		return p.LanguageModel(ctx, model)

	default:
		return nil, fmt.Errorf("unsupported provider: %q", opts.Provider)
	}
}
