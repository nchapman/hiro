// Package models provides model metadata lookups via catwalk's embedded
// provider registry. No network access is required — all data is bundled
// at compile time.
package models

import (
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
)

var (
	initOnce sync.Once
	registry map[string]catwalk.Model
)

const defaultContextWindow = 200_000

func ensureInit() {
	initOnce.Do(func() {
		providers := embedded.GetAll()
		registry = make(map[string]catwalk.Model, 128)
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
func Lookup(modelID string) (catwalk.Model, bool) {
	ensureInit()
	m, ok := registry[modelID]
	return m, ok
}

// ContextWindow returns the context window for a model.
// Returns defaultContextWindow (200K) if the model is unknown.
func ContextWindow(modelID string) int {
	m, ok := Lookup(modelID)
	if !ok || m.ContextWindow <= 0 {
		return defaultContextWindow
	}
	return int(m.ContextWindow)
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
