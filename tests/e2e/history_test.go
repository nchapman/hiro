//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestE2E_ConversationHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Send three distinct words across separate turns.
	cs.chat(ctx, "I'm going to tell you three words. First word: elephant.")
	cs.chat(ctx, "Second word: telescope.")
	cs.chat(ctx, "Third word: saxophone.")

	// Ask to recall all three.
	resp := cs.chat(ctx, "What were the three words I told you? List them.")
	lower := strings.ToLower(resp)
	for _, word := range []string{"elephant", "telescope", "saxophone"} {
		if !strings.Contains(lower, word) {
			t.Errorf("expected %q in response, got %q", word, resp)
		}
	}
	t.Logf("History recall: %s", resp)
}

func TestE2E_MessagesAPI(t *testing.T) {
	// The operator should have messages from other tests (or at minimum
	// from startup). Verify the REST endpoint returns them.
	url := fmt.Sprintf("%s/api/instances/%s/messages", baseURL, operatorID)
	resp, err := httpClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var messages []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		t.Fatalf("decoding messages: %v", err)
	}
	if len(messages) == 0 {
		t.Error("expected at least one message from operator history")
	}
	t.Logf("Messages API returned %d messages", len(messages))
}
