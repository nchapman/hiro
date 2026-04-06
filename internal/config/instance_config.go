package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const instanceConfigFileName = "config.yaml"

// InstanceConfig holds per-instance operational configuration.
// This file is root-owned with 0600 permissions — the agent worker
// process cannot read or modify it. Only the control plane reads and
// writes this file.
type InstanceConfig struct {
	Model           string                  `yaml:"model,omitempty"`
	ReasoningEffort string                  `yaml:"reasoning_effort,omitempty"`
	AllowedTools    []string                `yaml:"allowed_tools,omitempty"`
	DisallowedTools []string                `yaml:"disallowed_tools,omitempty"`
	Channels        *InstanceChannelsConfig `yaml:"channels,omitempty"`
}

// InstanceChannelsConfig holds per-instance channel bindings.
type InstanceChannelsConfig struct {
	Telegram *InstanceTelegramConfig `yaml:"telegram,omitempty"`
	Slack    *InstanceSlackConfig    `yaml:"slack,omitempty"`
}

// InstanceTelegramConfig configures a Telegram bot binding for an instance.
type InstanceTelegramConfig struct {
	BotToken     string  `yaml:"bot_token"`
	AllowedChats []int64 `yaml:"allowed_chats,omitempty"`
}

// InstanceSlackConfig configures a Slack app binding for an instance.
type InstanceSlackConfig struct {
	BotToken        string   `yaml:"bot_token"`
	SigningSecret   string   `yaml:"signing_secret"`
	AllowedChannels []string `yaml:"allowed_channels,omitempty"`
}

// LoadInstanceConfig reads the per-instance config.yaml from the given
// instance directory. Returns a zero-value InstanceConfig (not an error)
// if the file does not exist.
func LoadInstanceConfig(instDir string) (InstanceConfig, error) {
	path := filepath.Join(instDir, instanceConfigFileName)
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed from instance dir, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return InstanceConfig{}, nil
		}
		return InstanceConfig{}, fmt.Errorf("reading instance config: %w", err)
	}
	if len(data) == 0 {
		return InstanceConfig{}, nil
	}
	var cfg InstanceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return InstanceConfig{}, fmt.Errorf("parsing instance config: %w", err)
	}
	return cfg, nil
}

// SaveInstanceConfig writes the per-instance config.yaml to the given
// instance directory using atomic write (temp+rename) with 0600 permissions.
func SaveInstanceConfig(instDir string, cfg InstanceConfig) error {
	path := filepath.Join(instDir, instanceConfigFileName)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling instance config: %w", err)
	}
	return atomicWrite(path, data)
}

// IsInstanceConfigFile reports whether the given path (relative to an
// instance directory root) is the protected config.yaml file.
func IsInstanceConfigFile(path, instDir string) bool {
	return filepath.Base(path) == instanceConfigFileName && filepath.Dir(path) == instDir
}
