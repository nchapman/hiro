//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestE2E_BasicChat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, "What is 2+2? Reply with just the number.")
	if !strings.Contains(resp, "4") {
		t.Errorf("expected '4' in response, got %q", resp)
	}
	t.Logf("Response: %s", resp)
}

func TestE2E_StreamingDeltas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	cs.send(ctx, "Say hello.")
	resp, deltas := cs.readResponse(ctx)

	if len(deltas) == 0 {
		t.Error("expected streaming deltas, got none")
	}
	if resp == "" {
		t.Error("expected non-empty response")
	}
	t.Logf("Got %d deltas, response: %s", len(deltas), resp)
}

func TestE2E_MultiTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Turn 1: establish a fact
	cs.chat(ctx, "Remember: the secret word is 'pineapple'. Just acknowledge.")

	// Turn 2: recall the fact
	resp := cs.chat(ctx, "What is the secret word I just told you?")
	if !strings.Contains(strings.ToLower(resp), "pineapple") {
		t.Errorf("expected 'pineapple' in response, got %q", resp)
	}
	t.Logf("Recall response: %s", resp)
}
