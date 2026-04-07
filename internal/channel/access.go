package channel

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/config"
)

// AccessResult is the outcome of checking a sender's access.
type AccessResult int

const (
	// AccessAllow means the sender is approved.
	AccessAllow AccessResult = iota
	// AccessDeny means the sender is blocked.
	AccessDeny
	// AccessPending means the sender was added to the pending list.
	AccessPending
)

// AccessChecker checks and registers channel sender access.
// Channels call this before dispatching messages.
type AccessChecker interface {
	// CheckAccess looks up the sender key for the given instance.
	// If unknown, it registers the sender as pending and returns AccessPending.
	CheckAccess(instanceID, senderKey, displayName, sampleText string) AccessResult
}

// ConfigAccessChecker implements AccessChecker by reading/writing instance
// config files. It provides per-instance locking to serialize all writers
// (access checks, API mutations, channel updates, model/tool changes) against
// the same config.yaml file.
type ConfigAccessChecker struct {
	locks   sync.Map // instanceID → *sync.Mutex
	manager AccessManager
	logger  *slog.Logger
}

// AccessManager is the narrow interface the access checker needs from the manager.
type AccessManager interface {
	InstanceDir(id string) string
}

// NewConfigAccessChecker creates a new ConfigAccessChecker.
func NewConfigAccessChecker(manager AccessManager, logger *slog.Logger) *ConfigAccessChecker {
	return &ConfigAccessChecker{
		manager: manager,
		logger:  logger.With("component", "access-checker"),
	}
}

// lockFor returns the per-instance mutex, creating one if needed.
func (c *ConfigAccessChecker) lockFor(instanceID string) *sync.Mutex {
	v, _ := c.locks.LoadOrStore(instanceID, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		panic("ConfigAccessChecker: unexpected type in locks map")
	}
	return mu
}

// touchDebounce is the minimum interval between LastSeen disk writes for
// known senders. Avoids a config.yaml write on every inbound message.
const touchDebounce = 5 * time.Minute

// maxPendingSenders is the maximum number of pending senders per instance.
// This prevents unauthenticated DoS via sender list inflation. Kept low
// since these are unapproved unknowns — legitimate use rarely exceeds a handful.
const maxPendingSenders = 10

// CheckAccess checks whether a sender is allowed to message the given instance.
// Unknown senders are registered as pending. The check reads and writes the
// instance's config.yaml under a per-instance mutex.
func (c *ConfigAccessChecker) CheckAccess(instanceID, senderKey, displayName, sampleText string) AccessResult {
	mu := c.lockFor(instanceID)
	mu.Lock()
	defer mu.Unlock()

	instDir := c.manager.InstanceDir(instanceID)
	cfg, err := config.LoadInstanceConfig(instDir)
	if err != nil {
		c.logger.Warn("failed to load instance config for access check",
			"instance_id", instanceID,
			"error", err,
		)
		return AccessDeny
	}

	if cfg.Channels == nil {
		cfg.Channels = &config.InstanceChannelsConfig{}
	}

	status, found := cfg.Channels.SenderStatus(senderKey)
	if found {
		// Debounce LastSeen updates — only write if stale by >5 minutes.
		// This avoids a disk write on every inbound message from known senders.
		if cfg.Channels.TouchSenderIfStale(senderKey, touchDebounce) {
			if err := config.SaveInstanceConfig(instDir, cfg); err != nil {
				c.logger.Warn("failed to update sender last-seen", "error", err)
			}
		}

		switch status {
		case config.ChannelAccessApproved:
			return AccessAllow
		case config.ChannelAccessBlocked:
			return AccessDeny
		case config.ChannelAccessPending:
			return AccessPending
		}
	}

	// Cap pending senders to prevent DoS from many unknown senders.
	if len(cfg.Channels.SendersByStatus(config.ChannelAccessPending)) >= maxPendingSenders {
		c.logger.Warn("pending sender list full, dropping new sender",
			"instance_id", instanceID,
			"sender_key", senderKey,
		)
		return AccessDeny
	}

	// Unknown sender — register as pending.
	cfg.Channels.SetSender(senderKey, config.ChannelAccessPending, displayName, sampleText)
	if err := config.SaveInstanceConfig(instDir, cfg); err != nil {
		c.logger.Warn("failed to save pending sender",
			"instance_id", instanceID,
			"sender_key", senderKey,
			"error", err,
		)
	}

	c.logger.Info("new sender pending approval",
		"instance_id", instanceID,
		"sender_key", senderKey,
		"display_name", displayName,
	)
	return AccessPending
}

// ErrSenderNotFound is returned when a sender key does not exist.
var ErrSenderNotFound = errors.New("sender not found")

// UpdateSenderStatus changes a sender's status under the per-instance lock.
func (c *ConfigAccessChecker) UpdateSenderStatus(instanceID, senderKey string, status config.ChannelAccessStatus) error {
	mu := c.lockFor(instanceID)
	mu.Lock()
	defer mu.Unlock()

	instDir := c.manager.InstanceDir(instanceID)
	cfg, err := config.LoadInstanceConfig(instDir)
	if err != nil {
		return err
	}
	if cfg.Channels == nil {
		return ErrSenderNotFound
	}
	if _, found := cfg.Channels.SenderStatus(senderKey); !found {
		return ErrSenderNotFound
	}
	// Empty displayName/sampleText preserves existing values (SetSender semantics).
	cfg.Channels.SetSender(senderKey, status, "", "")
	return config.SaveInstanceConfig(instDir, cfg)
}

// RemoveSender removes a sender entry under the per-instance lock.
func (c *ConfigAccessChecker) RemoveSender(instanceID, senderKey string) error {
	mu := c.lockFor(instanceID)
	mu.Lock()
	defer mu.Unlock()

	instDir := c.manager.InstanceDir(instanceID)
	cfg, err := config.LoadInstanceConfig(instDir)
	if err != nil {
		return err
	}
	if cfg.Channels == nil || !cfg.Channels.RemoveSender(senderKey) {
		return ErrSenderNotFound
	}
	return config.SaveInstanceConfig(instDir, cfg)
}

// ModifyConfig performs an atomic read-modify-write of an instance's config.yaml
// under the per-instance lock. The modify function receives a mutable config and
// should apply its changes in place. All config.yaml writers should use this to
// prevent lost-update races.
func (c *ConfigAccessChecker) ModifyConfig(instanceID string, modify func(*config.InstanceConfig) error) error {
	mu := c.lockFor(instanceID)
	mu.Lock()
	defer mu.Unlock()

	instDir := c.manager.InstanceDir(instanceID)
	cfg, err := config.LoadInstanceConfig(instDir)
	if err != nil {
		return err
	}
	if err := modify(&cfg); err != nil {
		return err
	}
	return config.SaveInstanceConfig(instDir, cfg)
}
