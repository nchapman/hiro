//go:build !linux

package landlock

import "errors"

// Probe returns an error on non-Linux platforms since Landlock is Linux-only.
func Probe() error {
	return errors.New("landlock not available: not running on Linux")
}

// Restrict returns an error on non-Linux platforms since Landlock is Linux-only.
func Restrict(rw, ro []string) error {
	return errors.New("landlock not available: not running on Linux")
}
