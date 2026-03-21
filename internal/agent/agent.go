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

// Chat sends a message to the agent and returns the response text.
func (a *Agent) Chat(ctx context.Context, prompt string) (string, error) {
	result, err := a.agent.Generate(ctx, fantasy.AgentCall{
		Prompt: prompt,
	})
	if err != nil {
		return "", fmt.Errorf("agent generate: %w", err)
	}
	return result.Response.Content.Text(), nil
}

// StreamChat sends a message to the agent and streams the response.
// The onDelta callback is called with each text token as it arrives.
// If onDelta returns an error, streaming stops and the error is propagated.
// Returns the complete response text when done.
func (a *Agent) StreamChat(ctx context.Context, prompt string, onDelta func(text string) error) (string, error) {
	result, err := a.agent.Stream(ctx, fantasy.AgentStreamCall{
		Prompt: prompt,
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
