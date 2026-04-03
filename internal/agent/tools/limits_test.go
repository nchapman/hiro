package tools

import (
	"testing"
	"time"
)

func TestLimitsConstants(t *testing.T) {
	// Verify key limit relationships hold.
	if outputTruncateHalf != maxOutputLen/2 {
		t.Errorf("outputTruncateHalf = %d, want %d", outputTruncateHalf, maxOutputLen/2)
	}
	if autoBackgroundAfter <= 0 {
		t.Error("autoBackgroundAfter must be positive")
	}
	if grepTimeout <= 0 {
		t.Error("grepTimeout must be positive")
	}
	if maxGlobResults <= 0 {
		t.Error("maxGlobResults must be positive")
	}
	if MaxBackgroundJobs <= 0 {
		t.Error("MaxBackgroundJobs must be positive")
	}
	if completedJobRetention < time.Hour {
		t.Errorf("completedJobRetention = %v, expected at least 1 hour", completedJobRetention)
	}
}
