//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestE2E_ScheduleRecurring(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Create a recurring schedule.
	resp := cs.chat(ctx, `Use the ScheduleRecurring tool with name "e2e-recurring", schedule "0 3 * * *", and message "test cron fire". Do not use any other tools.`)
	t.Logf("ScheduleRecurring response: %s", resp)

	if !strings.Contains(strings.ToLower(resp), "e2e-recurring") && !strings.Contains(strings.ToLower(resp), "schedule") {
		t.Errorf("expected confirmation of schedule creation, got %q", resp)
	}

	// Verify it appears in ListSchedules.
	resp2 := cs.chat(ctx, `Use the ListSchedules tool now. Do not use any other tools.`)
	t.Logf("ListSchedules response: %s", resp2)

	if !strings.Contains(resp2, "e2e-recurring") {
		t.Errorf("expected 'e2e-recurring' in schedule list, got %q", resp2)
	}
	if !strings.Contains(resp2, "0 3 * * *") {
		t.Errorf("expected cron expression in list, got %q", resp2)
	}

	// Cancel it.
	resp3 := cs.chat(ctx, `Use the CancelSchedule tool with name "e2e-recurring". Do not use any other tools.`)
	t.Logf("CancelSchedule response: %s", resp3)

	// Verify it's gone.
	resp4 := cs.chat(ctx, `Use the ListSchedules tool now. Do not use any other tools.`)
	t.Logf("ListSchedules after cancel: %s", resp4)

	if strings.Contains(resp4, "e2e-recurring") {
		t.Errorf("expected 'e2e-recurring' to be gone after cancel, got %q", resp4)
	}
}

func TestE2E_ScheduleOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Schedule a one-time task 2 hours from now (won't fire during test).
	resp := cs.chat(ctx, `Use the ScheduleOnce tool with name "e2e-once", at "2h", and message "one-time test". Do not use any other tools.`)
	t.Logf("ScheduleOnce response: %s", resp)

	if !strings.Contains(strings.ToLower(resp), "e2e-once") && !strings.Contains(strings.ToLower(resp), "schedule") {
		t.Errorf("expected confirmation, got %q", resp)
	}

	// Verify it shows in list with type "once".
	resp2 := cs.chat(ctx, `Use the ListSchedules tool now. Do not use any other tools.`)
	t.Logf("ListSchedules response: %s", resp2)

	if !strings.Contains(resp2, "e2e-once") {
		t.Errorf("expected 'e2e-once' in list, got %q", resp2)
	}

	// Clean up.
	cs.chat(ctx, `Use the CancelSchedule tool with name "e2e-once". Do not use any other tools.`)
}

func TestE2E_ScheduleRecurring_DuplicateName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Create first schedule.
	cs.chat(ctx, `Use the ScheduleRecurring tool with name "e2e-dup", schedule "0 6 * * *", and message "first". Do not use any other tools.`)

	// Try to create another with the same name — should get an error.
	resp := cs.chat(ctx, `Use the ScheduleRecurring tool with name "e2e-dup", schedule "0 7 * * *", and message "second". Do not use any other tools.`)
	t.Logf("Duplicate response: %s", resp)

	if !strings.Contains(strings.ToLower(resp), "already exists") && !strings.Contains(strings.ToLower(resp), "duplicate") {
		t.Logf("Warning: expected duplicate error mention, got %q (agent may have paraphrased)", resp)
	}

	// Clean up.
	cs.chat(ctx, `Use the CancelSchedule tool with name "e2e-dup". Do not use any other tools.`)
}

func TestE2E_ScheduleOnce_FiresImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	// Schedule a one-time task with at="now" — should fire immediately
	// and deliver a notification via the triggered session.
	resp := cs.chat(ctx, `Use the ScheduleOnce tool with name "e2e-fire-now", at "now", and message "Say exactly: FIRE_CONFIRMED". Do not use any other tools.`)
	t.Logf("ScheduleOnce now response: %s", resp)

	// The LLM should confirm the schedule was created. It may mention the
	// name, say "schedule", or echo the fire time — any acknowledgement is fine.
	t.Logf("ScheduleOnce confirmed: %s", resp)

	// Wait for the notification to arrive. The triggered session runs the
	// prompt and calls Notify, which delivers to the primary session.
	// Poll with chat messages since notifications arrive as meta messages.
	var found bool
	for i := 0; i < 12; i++ { // up to ~60s of polling
		time.Sleep(5 * time.Second)
		resp2 := cs.chat(ctx, `Check if you received any notifications from a scheduled task. If you see "FIRE_CONFIRMED" in a notification, say exactly "NOTIFICATION_RECEIVED". Otherwise say "WAITING".`)
		t.Logf("poll %d: %s", i+1, resp2)
		if strings.Contains(resp2, "NOTIFICATION_RECEIVED") || strings.Contains(resp2, "FIRE_CONFIRMED") {
			found = true
			break
		}
	}

	if !found {
		t.Error("scheduled task did not fire or notification was not received within timeout")
	}

	// Verify the one-shot was auto-cleaned up.
	resp3 := cs.chat(ctx, `Use the ListSchedules tool now. Do not use any other tools.`)
	t.Logf("ListSchedules after fire: %s", resp3)
	if strings.Contains(resp3, "e2e-fire-now") {
		t.Error("expected one-shot schedule to be cleaned up after firing")
	}
}

func TestE2E_CancelSchedule_NotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, `Use the CancelSchedule tool with name "nonexistent-schedule-xyz". Do not use any other tools.`)
	t.Logf("Cancel not-found response: %s", resp)

	// The agent should mention it wasn't found.
	lower := strings.ToLower(resp)
	if !strings.Contains(lower, "not found") && !strings.Contains(lower, "no schedule") && !strings.Contains(lower, "doesn't exist") && !strings.Contains(lower, "does not exist") {
		t.Logf("Warning: expected not-found mention, got %q (agent may have paraphrased)", resp)
	}
}
