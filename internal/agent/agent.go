// Package agent provides the Hive agent runtime. The Manager supervises
// agent session lifecycles while the inference package handles the LLM loop.
package agent

import (
	"context"
	"fmt"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"
)

// ProviderType identifies which LLM provider to use.
type ProviderType string

const (
	ProviderAnthropic  ProviderType = "anthropic"
	ProviderOpenRouter ProviderType = "openrouter"
)

// Options configures the Manager.
type Options struct {
	WorkingDir string // working directory for file/bash tools
	Model      string // override model for all agents (from HIVE_MODEL)
}

// CreateLanguageModel creates a language model for the given provider and model name.
func CreateLanguageModel(ctx context.Context, provider ProviderType, apiKey, model string) (fantasy.LanguageModel, error) {
	switch provider {
	case ProviderAnthropic:
		p, err := anthropic.New(anthropic.WithAPIKey(apiKey))
		if err != nil {
			return nil, err
		}
		return p.LanguageModel(ctx, model)

	case ProviderOpenRouter:
		p, err := openrouter.New(openrouter.WithAPIKey(apiKey))
		if err != nil {
			return nil, err
		}
		return p.LanguageModel(ctx, model)

	default:
		return nil, fmt.Errorf("unsupported provider: %q", provider)
	}
}

// TestProviderConnection validates a provider's API key by sending a minimal request.
func TestProviderConnection(ctx context.Context, provider ProviderType, apiKey, model string) error {
	lm, err := CreateLanguageModel(ctx, provider, apiKey, model)
	if err != nil {
		return err
	}

	maxTokens := int64(1)
	_, err = lm.Generate(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt{fantasy.NewUserMessage("Hi")},
		MaxOutputTokens: &maxTokens,
	})
	return err
}
