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
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"

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
	config     config.AgentConfig
	agent      fantasy.Agent
	workingDir string
	logger     *slog.Logger
}

// Options configures how an agent connects to an LLM provider.
type Options struct {
	Provider   ProviderType
	APIKey     string
	Model      string                // overrides the model from agent config
	WorkingDir string                // working directory for file/bash tools
	ExtraTools []fantasy.AgentTool   // additional tools injected by the manager
	Identity   string                // instance identity (from identity.md)
	LM         fantasy.LanguageModel // if set, bypasses provider creation (for testing)
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
		config:     cfg,
		workingDir: workingDir,
		logger:     logger,
	}

	agentTools := a.buildTools()
	agentTools = append(agentTools, opts.ExtraTools...)

	systemPrompt := buildSystemPrompt(cfg, opts.Identity)

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

// buildSystemPrompt assembles the system prompt from the agent's config
// and optional instance identity. Order: soul → identity → instructions → tools → skills.
func buildSystemPrompt(cfg config.AgentConfig, identity string) string {
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

	p.WriteString(cfg.Prompt)

	if cfg.Tools != "" {
		p.WriteString("\n\n## Tool Notes\n\n")
		p.WriteString(cfg.Tools)
	}

	if len(cfg.Skills) > 0 {
		p.WriteString("\n\n## Skills\n\n")
		for _, skill := range cfg.Skills {
			fmt.Fprintf(&p, "### %s\n%s\n\n%s\n\n", skill.Name, skill.Description, skill.Prompt)
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
