//go:build !linux

package netiso

import (
	"context"
	"fmt"
	"log/slog"
)

// NetIso is a stub on non-Linux platforms.
type NetIso struct{}

// Probe always returns an error on non-Linux platforms.
func Probe() error {
	return fmt.Errorf("network isolation requires Linux")
}

// New is not available on non-Linux platforms.
func New(_ *slog.Logger) (*NetIso, error) {
	return nil, fmt.Errorf("network isolation requires Linux")
}

// Setup is not available on non-Linux platforms.
func (n *NetIso) Setup(_ context.Context, _ AgentNetwork) error {
	return fmt.Errorf("network isolation requires Linux")
}

// Teardown is a no-op on non-Linux platforms.
func (n *NetIso) Teardown(_ string) error {
	return nil
}

// Close is a no-op on non-Linux platforms.
func (n *NetIso) Close() error {
	return nil
}
