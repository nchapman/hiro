package cluster

import (
	"crypto/rand"
	"fmt"
)

// GenerateSwarmCode returns a human-friendly swarm code like "hk7m-q3fx".
// Uses lowercase alphanumeric characters excluding ambiguous ones (0, o, 1, l).
func GenerateSwarmCode() (string, error) {
	const charset = "abcdefghjkmnpqrstuvwxyz23456789"
	// Rejection threshold: largest multiple of len(charset) that fits in a byte.
	// This eliminates modulo bias.
	threshold := byteRange - (byteRange % len(charset))
	code := make([]byte, swarmCodeLen)
	var buf [1]byte
	for i := 0; i < len(code); {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("generating swarm code: %w", err)
		}
		if int(buf[0]) < threshold {
			code[i] = charset[int(buf[0])%len(charset)]
			i++
		}
	}
	// Format as xxxx-xxxx for readability.
	return string(code[:4]) + "-" + string(code[4:]), nil
}
