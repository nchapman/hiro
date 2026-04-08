//go:build !linux

package main

import "github.com/nchapman/hiro/internal/ipc"

func selfConfigureNetwork(_ ipc.SpawnConfig) error { return nil }
func waitForVethReady()                            {}
func installSeccomp() error                        { return nil }
