package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const instanceConfigFileName = "config.yaml"

// InstanceConfigLocker serializes read-modify-write operations on an instance's
// config.yaml. All writers that modify instance config must go through this
// interface to prevent lost-update races.
type InstanceConfigLocker interface {
	// ModifyConfig loads the instance's config.yaml, calls modify with a mutable
	// pointer, and saves the result — all under a per-instance lock.
	ModifyConfig(instanceID string, modify func(*InstanceConfig) error) error
}

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
	Senders  []ChannelSender         `yaml:"senders,omitempty"`
}

// InstanceTelegramConfig configures a Telegram bot binding for an instance.
type InstanceTelegramConfig struct {
	BotToken string `yaml:"bot_token"`
}

// InstanceSlackConfig configures a Slack app binding for an instance.
type InstanceSlackConfig struct {
	BotToken      string `yaml:"bot_token"`
	SigningSecret string `yaml:"signing_secret"`
}

// ChannelAccessStatus represents the approval state of a channel sender.
type ChannelAccessStatus string

const (
	ChannelAccessApproved ChannelAccessStatus = "approved"
	ChannelAccessPending  ChannelAccessStatus = "pending"
	ChannelAccessBlocked  ChannelAccessStatus = "blocked"
)

// ChannelSender holds metadata about a known channel sender (chat or channel).
// The Key format is "tg:<chatID>" for Telegram or "slack:<channelID>" for Slack.
type ChannelSender struct {
	Key         string              `yaml:"key" json:"key"`
	DisplayName string              `yaml:"display_name,omitempty" json:"display_name"`
	Status      ChannelAccessStatus `yaml:"status" json:"status"`
	FirstSeen   time.Time           `yaml:"first_seen" json:"first_seen"`
	LastSeen    time.Time           `yaml:"last_seen" json:"last_seen"`
	SampleText  string              `yaml:"sample_text,omitempty" json:"sample_text,omitempty"`
}

// SenderStatus returns the status of a sender by key, and whether it was found.
func (c *InstanceChannelsConfig) SenderStatus(key string) (ChannelAccessStatus, bool) {
	for _, s := range c.Senders {
		if s.Key == key {
			return s.Status, true
		}
	}
	return "", false
}

// SendersByStatus returns all senders with the given status.
func (c *InstanceChannelsConfig) SendersByStatus(status ChannelAccessStatus) []ChannelSender {
	var result []ChannelSender
	for _, s := range c.Senders {
		if s.Status == status {
			result = append(result, s)
		}
	}
	return result
}

// SetSender adds or updates a sender entry. For existing entries, only non-empty
// displayName and sampleText values overwrite existing ones — empty values
// preserve the current values. Status and LastSeen are always updated. If the
// key does not exist, a new entry is created with FirstSeen = now.
func (c *InstanceChannelsConfig) SetSender(key string, status ChannelAccessStatus, displayName, sampleText string) {
	now := time.Now().UTC()
	for i := range c.Senders {
		if c.Senders[i].Key != key {
			continue
		}
		c.Senders[i].Status = status
		c.Senders[i].LastSeen = now
		if displayName != "" {
			c.Senders[i].DisplayName = displayName
		}
		if sampleText != "" {
			c.Senders[i].SampleText = sampleText
		}
		return
	}
	c.Senders = append(c.Senders, ChannelSender{
		Key:         key,
		DisplayName: displayName,
		Status:      status,
		FirstSeen:   now,
		LastSeen:    now,
		SampleText:  sampleText,
	})
}

// TouchSender updates the LastSeen time for a sender.
func (c *InstanceChannelsConfig) TouchSender(key string) {
	now := time.Now().UTC()
	for i := range c.Senders {
		if c.Senders[i].Key == key {
			c.Senders[i].LastSeen = now
			return
		}
	}
}

// TouchSenderIfStale updates LastSeen only if it is older than maxAge.
// Returns true if the timestamp was updated (caller should persist).
func (c *InstanceChannelsConfig) TouchSenderIfStale(key string, maxAge time.Duration) bool {
	now := time.Now().UTC()
	for i := range c.Senders {
		if c.Senders[i].Key == key {
			if now.Sub(c.Senders[i].LastSeen) > maxAge {
				c.Senders[i].LastSeen = now
				return true
			}
			return false
		}
	}
	return false
}

// RemoveSender removes a sender entry by key. Returns true if found and removed.
func (c *InstanceChannelsConfig) RemoveSender(key string) bool {
	for i := range c.Senders {
		if c.Senders[i].Key == key {
			c.Senders = append(c.Senders[:i], c.Senders[i+1:]...)
			return true
		}
	}
	return false
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
