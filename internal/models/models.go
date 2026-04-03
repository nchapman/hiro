// Package models provides model metadata lookups via catwalk's embedded
// provider registry. No network access is required — all data is bundled
// at compile time.
package models

import (
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
)

var (
	initOnce sync.Once
	registry map[string]catwalk.Model
)

const (
	// DefaultContextWindow is the assumed context window size when a model
	// does not report one.
	DefaultContextWindow = 200_000

	// registryInitialCap is the initial capacity for the model registry map.
	registryInitialCap = 128

	// modelsPerProviderEstimate is the estimated number of models per provider,
	// used to pre-allocate result slices.
	modelsPerProviderEstimate = 10
)

func ensureInit() {
	initOnce.Do(func() {
		providers := embedded.GetAll()
		registry = make(map[string]catwalk.Model, registryInitialCap)
		for _, p := range providers {
			for _, m := range p.Models {
				// First writer wins — if the same model ID appears in
				// multiple providers, keep the first one.
				if _, exists := registry[m.ID]; !exists {
					registry[m.ID] = m
				}
			}
		}
	})
}

// Lookup finds a model by ID across all embedded providers.
// Accepts both raw model IDs ("x-ai/grok-4.1-fast") and "provider/model"
// spec strings ("openrouter/x-ai/grok-4.1-fast"). On a miss, strips the
// leading segment before the first "/" and retries recursively.
func Lookup(modelID string) (catwalk.Model, bool) {
	ensureInit()
	if m, ok := registry[modelID]; ok {
		return m, true
	}
	// Spec format: strip one prefix segment and retry.
	// Handles "openrouter/x-ai/grok-4.1-fast" → "x-ai/grok-4.1-fast" → found.
	if _, after, ok := strings.Cut(modelID, "/"); ok {
		return Lookup(after)
	}
	return catwalk.Model{}, false
}

// ContextWindow returns the context window for a model.
// Returns DefaultContextWindow (200K) if the model is unknown.
func ContextWindow(modelID string) int {
	m, ok := Lookup(modelID)
	if !ok || m.ContextWindow <= 0 {
		return DefaultContextWindow
	}
	return int(m.ContextWindow)
}

// ModelInfo is a simplified model description for API consumers.
type ModelInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider,omitempty"`
	CanReason       bool     `json:"can_reason"`
	ReasoningLevels []string `json:"reasoning_levels,omitempty"`
	ContextWindow   int64    `json:"context_window"`
}

// ModelsForProvider returns models available for the given provider type.
// The providerType matches catwalk's Provider.Type or Provider.ID (e.g. "anthropic", "openrouter").
func ModelsForProvider(providerType string) []ModelInfo {
	providers := embedded.GetAll()
	result := make([]ModelInfo, 0)
	for _, p := range providers {
		if string(p.Type) != providerType && string(p.ID) != providerType {
			continue
		}
		for _, m := range p.Models {
			if m.ContextWindow <= 0 {
				continue
			}
			info := ModelInfo{
				ID:            m.ID,
				Name:          m.Name,
				Provider:      string(p.ID),
				CanReason:     m.CanReason,
				ContextWindow: m.ContextWindow,
			}
			if len(m.ReasoningLevels) > 0 {
				info.ReasoningLevels = m.ReasoningLevels
			}
			result = append(result, info)
		}
	}
	return result
}

// ModelsForProviders returns models for multiple provider types.
func ModelsForProviders(providerTypes []string) []ModelInfo {
	result := make([]ModelInfo, 0, len(providerTypes)*modelsPerProviderEstimate)
	for _, pt := range providerTypes {
		result = append(result, ModelsForProvider(pt)...)
	}
	return result
}

// Cost computes the cost of a single LLM call in dollars.
func Cost(modelID string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	m, ok := Lookup(modelID)
	if !ok {
		return 0
	}
	return float64(inputTokens)*m.CostPer1MIn/1e6 +
		float64(outputTokens)*m.CostPer1MOut/1e6 +
		float64(cacheReadTokens)*m.CostPer1MInCached/1e6 +
		float64(cacheWriteTokens)*m.CostPer1MOutCached/1e6
}
