// Package provider handles LLM provider construction and configuration.
// It abstracts over multiple LLM backends (Anthropic, OpenAI, Google, etc.)
// so the rest of the codebase only depends on the fantasy.LanguageModel interface.
package provider

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

// Type identifies which LLM provider to use.
type Type string

// CreateLanguageModel creates a language model for the given provider and model name.
// The baseURL parameter allows overriding the provider's default API endpoint.
func CreateLanguageModel(ctx context.Context, provider Type, apiKey, baseURL, model string) (fantasy.LanguageModel, error) {
	// Look up the catwalk provider to determine the underlying type.
	cwProvider, cwType := lookupCatwalkProvider(string(provider))

	// resolveBaseURL picks the effective base URL: explicit override first,
	// then the catwalk-catalogued endpoint (if it's a real URL, not a placeholder).
	resolveBaseURL := func() string {
		if baseURL != "" {
			return baseURL
		}
		if cwProvider != nil && isRealURL(cwProvider.APIEndpoint) {
			return cwProvider.APIEndpoint
		}
		return ""
	}

	p, err := createProvider(cwType, apiKey, baseURL, resolveBaseURL)
	if err != nil {
		return nil, fmt.Errorf("creating %s provider: %w", provider, err)
	}
	return p.LanguageModel(ctx, model)
}

// createProvider constructs the fantasy.Provider for the given catwalk type.
func createProvider(cwType catwalk.Type, apiKey, baseURL string, resolveBaseURL func() string) (fantasy.Provider, error) {
	switch cwType {
	case catwalk.TypeAnthropic:
		return newWithBaseURL(anthropic.WithAPIKey(apiKey), anthropic.WithBaseURL, resolveBaseURL(), anthropic.New)

	case catwalk.TypeOpenAI:
		return newWithBaseURL(openai.WithAPIKey(apiKey), openai.WithBaseURL, resolveBaseURL(), openai.New)

	case catwalk.TypeOpenRouter:
		return openrouter.New(openrouter.WithAPIKey(apiKey))

	case catwalk.TypeGoogle, catwalk.TypeVertexAI:
		return newWithBaseURL(google.WithGeminiAPIKey(apiKey), google.WithBaseURL, baseURL, google.New)

	case catwalk.TypeAzure:
		return newWithBaseURL(azure.WithAPIKey(apiKey), azure.WithBaseURL, baseURL, azure.New)

	case catwalk.TypeBedrock:
		return newWithBaseURL(bedrock.WithAPIKey(apiKey), bedrock.WithBaseURL, baseURL, bedrock.New)

	case catwalk.TypeVercel:
		return newWithBaseURL(vercel.WithAPIKey(apiKey), vercel.WithBaseURL, baseURL, vercel.New)

	case catwalk.TypeOpenAICompat:
		return newWithBaseURL(openaicompat.WithAPIKey(apiKey), openaicompat.WithBaseURL, resolveBaseURL(), openaicompat.New)

	default:
		return nil, fmt.Errorf("unsupported provider: %q", cwType)
	}
}

// newWithBaseURL constructs a provider with an API key option and an optional
// base URL. This captures the common pattern across most provider constructors.
func newWithBaseURL[O any](keyOpt O, baseURLOpt func(string) O, url string, newFn func(...O) (fantasy.Provider, error)) (fantasy.Provider, error) {
	opts := []O{keyOpt}
	if url != "" {
		opts = append(opts, baseURLOpt(url))
	}
	return newFn(opts...)
}

// TestConnection validates a provider's API key by sending a minimal request.
func TestConnection(ctx context.Context, provider Type, apiKey, baseURL, model string) error {
	lm, err := CreateLanguageModel(ctx, provider, apiKey, baseURL, model)
	if err != nil {
		return err
	}

	_, err = lm.Generate(ctx, fantasy.Call{
		Prompt: fantasy.Prompt{fantasy.NewUserMessage("Reply with the word OK")},
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

// Info describes an available provider type.
type Info struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AvailableProviders returns all provider types that have embedded model data
// in catwalk and are supported by a fantasy adapter.
func AvailableProviders() []Info {
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

	var result []Info
	for _, p := range embedded.GetAll() {
		if !supported[p.Type] {
			continue
		}
		result = append(result, Info{
			ID:   string(p.ID),
			Name: p.Name,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
