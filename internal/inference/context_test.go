package inference

import (
	"context"
	"testing"
)

func TestCallerIDRoundtrip(t *testing.T) {
	ctx := context.Background()
	if got := callerIDFromContext(ctx); got != "" {
		t.Errorf("empty context should return empty caller ID, got %q", got)
	}

	ctx = ContextWithCallerID(ctx, "session-123")
	if got := callerIDFromContext(ctx); got != "session-123" {
		t.Errorf("got %q, want session-123", got)
	}
}

func TestCallChain_Detection(t *testing.T) {
	ctx := context.Background()

	if IsInCallChain(ctx, "A") {
		t.Error("empty context should not contain any session")
	}

	ctx = ContextWithCallChain(ctx, "A")
	if !IsInCallChain(ctx, "A") {
		t.Error("A should be in chain after adding")
	}
	if IsInCallChain(ctx, "B") {
		t.Error("B should not be in chain")
	}

	ctx = ContextWithCallChain(ctx, "B")
	if !IsInCallChain(ctx, "A") {
		t.Error("A should still be in chain")
	}
	if !IsInCallChain(ctx, "B") {
		t.Error("B should be in chain after adding")
	}
}

func TestCallChain_Immutable(t *testing.T) {
	ctx1 := ContextWithCallChain(context.Background(), "A")
	ctx2 := ContextWithCallChain(ctx1, "B")

	// ctx1's chain should not be modified by ctx2.
	if IsInCallChain(ctx1, "B") {
		t.Error("adding to child context should not modify parent")
	}
	if !IsInCallChain(ctx2, "A") {
		t.Error("child context should inherit parent chain")
	}
	if !IsInCallChain(ctx2, "B") {
		t.Error("child context should contain its own addition")
	}
}
