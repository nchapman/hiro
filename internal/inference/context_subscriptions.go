package inference

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// SubscriptionProvider returns a ContextProvider that shows active schedules
// in system reminders. Uses content-hash delta tracking.
func SubscriptionProvider(pdb *platformdb.DB, instanceID string) ContextProvider {
	if pdb == nil || instanceID == "" {
		return func(_ map[string]bool, _ []fantasy.Message) *DeltaResult { return nil }
	}
	return func(activeTools map[string]bool, history []fantasy.Message) *DeltaResult {
		if !activeTools["ScheduleRecurring"] {
			return nil
		}

		subs, err := pdb.ListSubscriptionsByInstance(context.Background(), instanceID)
		if err != nil || len(subs) == 0 {
			return nil
		}

		text := renderSubscriptionListing(subs)
		hash := contentHash(text)
		if replayLatestHash("subscriptions", history) == hash {
			return nil
		}

		return &DeltaResult{
			Message: buildContentMessage(text, "subscriptions", hash),
		}
	}
}

func renderSubscriptionListing(subs []platformdb.Subscription) string {
	var b strings.Builder
	b.WriteString("## Active Schedules\n\n")
	b.WriteString("| Name | Type | Schedule | Status | Next Fire |\n")
	b.WriteString("|------|------|----------|--------|-----------|\n")
	for _, s := range subs {
		nextFire := "—"
		if s.NextFire != nil {
			nextFire = s.NextFire.Format("2006-01-02 15:04")
		}
		schedule := s.Trigger.Expr
		if s.Trigger.Type == "once" {
			schedule = s.Trigger.At
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s | %s |\n",
			s.Name, s.Trigger.Type, schedule, s.Status, nextFire)
	}
	return b.String()
}
