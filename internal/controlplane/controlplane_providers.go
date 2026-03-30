package controlplane

import (
	"fmt"
	"sort"
	"strings"
)

// IsConfigured returns true if at least one provider with an API key exists.
func (cp *ControlPlane) IsConfigured() bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	for _, p := range cp.config.Providers {
		if p.APIKey != "" {
			return true
		}
	}
	return false
}

// ProviderInfo returns the default provider's type, API key, and base URL.
// This is the interface the Manager uses to resolve provider config.
// Uses DefaultProvider if set, otherwise falls back to the alphabetically
// first configured provider.
func (cp *ControlPlane) ProviderInfo() (providerType string, apiKey string, baseURL string, ok bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	// Use explicit default if set and configured.
	if dp := cp.config.DefaultProvider; dp != "" {
		if p, exists := cp.config.Providers[dp]; exists && p.APIKey != "" {
			return dp, p.APIKey, p.BaseURL, true
		}
	}

	// Fall back to the alphabetically first provider with an API key.
	names := make([]string, 0, len(cp.config.Providers))
	for name := range cp.config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cp.config.Providers[name]
		if p.APIKey != "" {
			return name, p.APIKey, p.BaseURL, true
		}
	}
	return "", "", "", false
}

// DefaultProvider returns the default provider type.
func (cp *ControlPlane) DefaultProvider() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.DefaultProvider
}

// SetDefaultProvider sets the default provider type.
func (cp *ControlPlane) SetDefaultProvider(providerType string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.DefaultProvider = providerType
}

// DefaultModel returns the global default model override.
func (cp *ControlPlane) DefaultModel() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.DefaultModel
}

// SetDefaultModel sets the global default model override.
func (cp *ControlPlane) SetDefaultModel(model string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.DefaultModel = model
}

// ProviderByType returns the API key and base URL for a specific provider type.
// Used by the Manager when an agent overrides the default provider.
func (cp *ControlPlane) ProviderByType(providerType string) (apiKey string, baseURL string, ok bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	p, exists := cp.config.Providers[providerType]
	if !exists || p.APIKey == "" {
		return "", "", false
	}
	return p.APIKey, p.BaseURL, true
}

// ConfiguredProviderTypes returns a sorted list of all configured provider type keys.
func (cp *ControlPlane) ConfiguredProviderTypes() []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	types := make([]string, 0, len(cp.config.Providers))
	for k, v := range cp.config.Providers {
		if v.APIKey != "" {
			types = append(types, k)
		}
	}
	sort.Strings(types)
	return types
}

// GetProvider returns a provider by type name.
func (cp *ControlPlane) GetProvider(providerType string) (ProviderConfig, bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	p, ok := cp.config.Providers[providerType]
	return p, ok
}

// SetProvider creates or updates a provider keyed by type.
// Returns an error if providerType or APIKey is empty.
func (cp *ControlPlane) SetProvider(providerType string, cfg ProviderConfig) error {
	if providerType == "" {
		return fmt.Errorf("provider type is required")
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("API key is required")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Providers[providerType] = cfg
	return nil
}

// DeleteProvider removes a provider by type.
func (cp *ControlPlane) DeleteProvider(providerType string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Providers, providerType)
	// Clear default if we just deleted it.
	if cp.config.DefaultProvider == providerType {
		cp.config.DefaultProvider = ""
	}
}

// ListProviders returns a copy of all providers with API keys masked
// (only last 4 characters visible). Suitable for API responses.
func (cp *ControlPlane) ListProviders() map[string]ProviderConfig {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	result := make(map[string]ProviderConfig, len(cp.config.Providers))
	for k, v := range cp.config.Providers {
		v.APIKey = maskKey(v.APIKey)
		result[k] = v
	}
	return result
}

// maskKey returns a masked version of an API key showing a short prefix
// and the last 4 characters (e.g. "sk-or-...4xBq").
func maskKey(key string) string {
	const prefixLen, suffixLen = 6, 4
	if len(key) < prefixLen+suffixLen+1 {
		return strings.Repeat("*", len(key))
	}
	return key[:prefixLen] + "..." + key[len(key)-suffixLen:]
}
