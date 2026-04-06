package controlplane

import "strings"

// TelegramConfig returns the Telegram channel configuration, or nil if not configured.
func (cp *ControlPlane) TelegramConfig() *TelegramChannelConfig {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Channels.Telegram
}

// ResolveSecret resolves a value that may be a ${SECRET_NAME} reference.
// If the value starts with "${" and ends with "}", the inner name is looked up
// in the secrets store. Otherwise the value is returned as-is. Returns empty
// string if the secret is not found.
func (cp *ControlPlane) ResolveSecret(value string) string {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return value
	}
	name := value[2 : len(value)-1]

	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Secrets[name]
}
