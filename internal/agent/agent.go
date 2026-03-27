// Package agent provides the Hive agent runtime. The Manager supervises
// agent session lifecycles while the inference package handles the LLM loop.
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
)

// isRealURL returns true if the endpoint is an actual URL, not a catwalk
// environment variable placeholder like "$ANTHROPIC_API_ENDPOINT".
func isRealURL(endpoint string) bool {
	return endpoint != "" && !strings.HasPrefix(endpoint, "$")
}

// ProviderType identifies which LLM provider to use.
type ProviderType string

// Options configures the Manager.
type Options struct {
	WorkingDir string // working directory for file/bash tools
	Model      string // override model for all agents (from HIVE_MODEL)
}

// CreateLanguageModel creates a language model for the given provider and model name.
// The baseURL parameter allows overriding the provider's default API endpoint.
func CreateLanguageModel(ctx context.Context, provider ProviderType, apiKey, baseURL, model string) (fantasy.LanguageModel, error) {
	// Look up the catwalk provider to determine the underlying type.
	cwProvider, cwType := lookupCatwalkProvider(string(provider))

	var (
		p   fantasy.Provider
		err error
	)

	switch cwType {
	case catwalk.TypeAnthropic:
		opts := []anthropic.Option{anthropic.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(baseURL))
		} else if cwProvider != nil && isRealURL(cwProvider.APIEndpoint) {
			opts = append(opts, anthropic.WithBaseURL(cwProvider.APIEndpoint))
		}
		p, err = anthropic.New(opts...)

	case catwalk.TypeOpenAI:
		opts := []openai.Option{openai.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		} else if cwProvider != nil && isRealURL(cwProvider.APIEndpoint) {
			opts = append(opts, openai.WithBaseURL(cwProvider.APIEndpoint))
		}
		p, err = openai.New(opts...)

	case catwalk.TypeOpenRouter:
		opts := []openrouter.Option{openrouter.WithAPIKey(apiKey)}
		p, err = openrouter.New(opts...)

	case catwalk.TypeGoogle, catwalk.TypeVertexAI:
		opts := []google.Option{google.WithGeminiAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, google.WithBaseURL(baseURL))
		}
		p, err = google.New(opts...)

	case catwalk.TypeAzure:
		opts := []azure.Option{azure.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, azure.WithBaseURL(baseURL))
		}
		p, err = azure.New(opts...)

	case catwalk.TypeBedrock:
		opts := []bedrock.Option{bedrock.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, bedrock.WithBaseURL(baseURL))
		}
		p, err = bedrock.New(opts...)

	case catwalk.TypeVercel:
		opts := []vercel.Option{vercel.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, vercel.WithBaseURL(baseURL))
		}
		p, err = vercel.New(opts...)

	case catwalk.TypeOpenAICompat:
		opts := []openaicompat.Option{openaicompat.WithAPIKey(apiKey)}
		if baseURL != "" {
			opts = append(opts, openaicompat.WithBaseURL(baseURL))
		} else if cwProvider != nil && isRealURL(cwProvider.APIEndpoint) {
			opts = append(opts, openaicompat.WithBaseURL(cwProvider.APIEndpoint))
		}
		p, err = openaicompat.New(opts...)

	default:
		return nil, fmt.Errorf("unsupported provider: %q", provider)
	}

	if err != nil {
		return nil, fmt.Errorf("creating %s provider: %w", provider, err)
	}
	return p.LanguageModel(ctx, model)
}

// TestProviderConnection validates a provider's API key by sending a minimal request.
func TestProviderConnection(ctx context.Context, provider ProviderType, apiKey, baseURL, model string) error {
	lm, err := CreateLanguageModel(ctx, provider, apiKey, baseURL, model)
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

// lookupCatwalkProvider finds the catwalk provider by ID and returns it along
// with its underlying type. If not found, returns nil and attempts to parse
// the provider string as a catwalk type directly.
func lookupCatwalkProvider(providerID string) (*catwalk.Provider, catwalk.Type) {
	for _, p := range embedded.GetAll() {
		if string(p.ID) == providerID {
			return &p, p.Type
		}
	}
	// Fall back: treat the providerID as a type directly (e.g. "openai-compat").
	return nil, catwalk.Type(providerID)
}

// TestModelForProvider returns a small/cheap model to use for connection testing.
// Uses catwalk's embedded DefaultSmallModelID when available.
func TestModelForProvider(providerType string) string {
	for _, p := range embedded.GetAll() {
		if string(p.ID) == providerType {
			return p.DefaultSmallModelID
		}
	}
	return ""
}

// ProviderInfo describes an available provider type.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AvailableProviders returns all provider types that have embedded model data
// in catwalk and are supported by a fantasy adapter.
func AvailableProviders() []ProviderInfo {
	// Set of catwalk types we can handle.
	supported := map[catwalk.Type]bool{
		catwalk.TypeAnthropic:    true,
		catwalk.TypeOpenAI:       true,
		catwalk.TypeOpenRouter:   true,
		catwalk.TypeGoogle:       true,
		catwalk.TypeVertexAI:     true,
		catwalk.TypeAzure:        true,
		catwalk.TypeBedrock:      true,
		catwalk.TypeVercel:       true,
		catwalk.TypeOpenAICompat: true,
	}

	var result []ProviderInfo
	for _, p := range embedded.GetAll() {
		if !supported[p.Type] {
			continue
		}
		result = append(result, ProviderInfo{
			ID:   string(p.ID),
			Name: p.Name,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
