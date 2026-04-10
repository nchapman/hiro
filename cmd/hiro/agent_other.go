//go:build !linux

package main

import "github.com/nchapman/hiro/internal/ipc"

func setNoNewPrivs() error                    { return nil }
func applyLandlock(_ ipc.LandlockPaths) error { return nil }
func installSeccomp(_ bool) error             { return nil }
