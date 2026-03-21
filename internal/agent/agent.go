// Package agent wraps charmbracelet/fantasy to provide the Hive agent runtime.
// An agent loads its identity and tools from markdown config, then runs an
// agentic loop that can delegate tasks to other agents in the swarm.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/hub"
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
	swarm          *hub.Swarm
	logger         *slog.Logger
	taskDispatcher TaskDispatchFunc
}

// Options configures how an agent connects to an LLM provider.
type Options struct {
	Provider ProviderType
	APIKey   string
	Model    string // overrides the model from agent config
}

// New creates a new Hive agent from the given config, connecting it to
// the swarm for task delegation.
func New(ctx context.Context, cfg config.AgentConfig, swarm *hub.Swarm, opts Options, logger *slog.Logger) (*Agent, error) {
	model := cfg.Model
	if opts.Model != "" {
		model = opts.Model
	}
	if model == "" {
		return nil, fmt.Errorf("no model specified for agent %q", cfg.Name)
	}

	lm, err := createLanguageModel(ctx, opts, model)
	if err != nil {
		return nil, fmt.Errorf("creating language model: %w", err)
	}

	a := &Agent{
		config: cfg,
		swarm:  swarm,
		logger: logger,
	}

	tools := a.buildTools()

	a.agent = fantasy.NewAgent(lm,
		fantasy.WithSystemPrompt(cfg.Prompt),
		fantasy.WithTools(tools...),
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
type Conversation struct {
	Messages []fantasy.Message
}

// NewConversation creates a new empty conversation.
func NewConversation() *Conversation {
	return &Conversation{}
}

// StreamChat sends a message in the context of a conversation, streaming
// the response token-by-token. The conversation history is automatically
// updated with both the user message and the assistant's response.
// If onDelta returns an error, streaming stops.
func (a *Agent) StreamChat(ctx context.Context, conv *Conversation, prompt string, onDelta func(text string) error) (string, error) {
	result, err := a.agent.Stream(ctx, fantasy.AgentStreamCall{
		Prompt:   prompt,
		Messages: conv.Messages,
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

	// Accumulate messages from all steps into conversation history
	conv.Messages = append(conv.Messages, fantasy.NewUserMessage(prompt))
	for _, step := range result.Steps {
		conv.Messages = append(conv.Messages, step.Messages...)
	}

	return result.Response.Content.Text(), nil
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
