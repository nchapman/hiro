//go:build !linux

package main

import "github.com/nchapman/hiro/internal/ipc"

func activateGroups(_ []uint32) error              { return nil }
func selfConfigureNetwork(_ ipc.SpawnConfig) error { return nil }
func waitForVethReady()                            {}
func installSeccomp() error                        { return nil }
