package cluster

import (
	"crypto/rand"
	"fmt"
)

// GenerateSwarmCode returns a human-friendly swarm code like "hk7m-q3fx".
// Uses lowercase alphanumeric characters excluding ambiguous ones (0, o, 1, l).
func GenerateSwarmCode() string {
	const charset = "abcdefghjkmnpqrstuvwxyz23456789"
	// Rejection threshold: largest multiple of len(charset) that fits in a byte.
	// This eliminates modulo bias.
	threshold := 256 - (256 % len(charset))
	code := make([]byte, 8)
	var buf [1]byte
	for i := 0; i < len(code); {
		if _, err := rand.Read(buf[:]); err != nil {
			panic(fmt.Sprintf("crypto/rand failed: %v", err))
		}
		if int(buf[0]) < threshold {
			code[i] = charset[int(buf[0])%len(charset)]
			i++
		}
	}
	// Format as xxxx-xxxx for readability.
	return string(code[:4]) + "-" + string(code[4:])
}
