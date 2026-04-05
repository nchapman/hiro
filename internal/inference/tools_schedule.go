package inference

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

//go:embed schedule_recurring.md
var scheduleRecurringDescription string

//go:embed schedule_once.md
var scheduleOnceDescription string

//go:embed cancel_schedule.md
var cancelScheduleDescription string

//go:embed list_schedules.md
var listSchedulesDescription string

// ScheduleCallback is called when a subscription is created or removed
// so the scheduler can update its heap. Implemented by the agent.Scheduler.
type ScheduleCallback interface {
	Add(sub platformdb.Subscription)
	Remove(id string)
}

const triggerTypeOnce = "once"

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func buildScheduleTools(pdb *platformdb.DB, schedulerCb ScheduleCallback, tz *time.Location) []Tool {
	if tz == nil {
		tz = time.UTC
	}

	return wrapAll([]fantasy.AgentTool{
		fantasy.NewAgentTool("ScheduleRecurring",
			scheduleRecurringDescription,
			func(ctx context.Context, input struct {
				Name     string `json:"name" description:"Unique name for this schedule (e.g. 'daily-report', 'health-check')."`
				Schedule string `json:"schedule" description:"Cron expression (e.g. '0 9 * * *' for 9am daily)."`
				Message  string `json:"message" description:"The prompt you will receive each time this fires."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleScheduleRecurring(ctx, pdb, schedulerCb, tz, input.Name, input.Schedule, input.Message)
			},
		),

		fantasy.NewAgentTool("ScheduleOnce",
			scheduleOnceDescription,
			func(ctx context.Context, input struct {
				Name    string `json:"name" description:"Unique name for this schedule (e.g. 'end-of-day-summary')."`
				At      string `json:"at" description:"When to fire: relative duration ('20m', '2h') or absolute time ('2026-04-05T17:00:00')."`
				Message string `json:"message" description:"The prompt you will receive when this fires."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleScheduleOnce(ctx, pdb, schedulerCb, tz, input.Name, input.At, input.Message)
			},
		),

		fantasy.NewAgentTool("CancelSchedule",
			cancelScheduleDescription,
			func(ctx context.Context, input struct {
				Name string `json:"name" description:"Name of the schedule to cancel."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleCancelSchedule(ctx, pdb, schedulerCb, input.Name)
			},
		),

		fantasy.NewAgentTool("ListSchedules",
			listSchedulesDescription,
			func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleListSchedules(ctx, pdb)
			},
		),
	})
}

func handleScheduleRecurring(ctx context.Context, pdb *platformdb.DB, schedulerCb ScheduleCallback, tz *time.Location, name, schedule, message string) (fantasy.ToolResponse, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return fantasy.NewTextErrorResponse("name is required"), nil
	}
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return fantasy.NewTextErrorResponse("schedule is required"), nil
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fantasy.NewTextErrorResponse("message is required"), nil
	}

	// Validate cron expression.
	sched, err := cronParser.Parse(schedule)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid cron expression %q: %v", schedule, err)), nil
	}

	callerID := callerIDFromContext(ctx)
	if callerID == "" {
		return fantasy.NewTextErrorResponse("no caller instance context"), nil
	}

	// Compute next fire time.
	nextFire := sched.Next(time.Now().In(tz))

	sub := platformdb.Subscription{
		ID:         uuid.Must(uuid.NewV7()).String(),
		InstanceID: callerID,
		Name:       name,
		Trigger:    platformdb.TriggerDef{Type: "cron", Expr: schedule},
		Message:    message,
		Status:     "active",
		NextFire:   &nextFire,
	}

	if err := pdb.CreateSubscription(ctx, sub); err != nil {
		if errors.Is(err, platformdb.ErrDuplicate) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("a schedule named %q already exists — cancel it first or choose a different name", name)), nil
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to create subscription: %v", err)), nil
	}

	// Notify scheduler.
	if schedulerCb != nil {
		schedulerCb.Add(sub)
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Schedule %q created. Next fire: %s", name, nextFire.Format("2006-01-02 15:04 MST"))), nil
}

func handleScheduleOnce(ctx context.Context, pdb *platformdb.DB, schedulerCb ScheduleCallback, tz *time.Location, name, at, message string) (fantasy.ToolResponse, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return fantasy.NewTextErrorResponse("name is required"), nil
	}
	at = strings.TrimSpace(at)
	if at == "" {
		return fantasy.NewTextErrorResponse("at is required"), nil
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fantasy.NewTextErrorResponse("message is required"), nil
	}

	callerID := callerIDFromContext(ctx)
	if callerID == "" {
		return fantasy.NewTextErrorResponse("no caller instance context"), nil
	}

	// Parse the time: try relative duration first, then absolute.
	fireAt, err := parseScheduleTime(at, tz)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid time %q: %v", at, err)), nil
	}
	if !fireAt.After(time.Now()) {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("time %q is in the past", at)), nil
	}

	sub := platformdb.Subscription{
		ID:         uuid.Must(uuid.NewV7()).String(),
		InstanceID: callerID,
		Name:       name,
		Trigger:    platformdb.TriggerDef{Type: triggerTypeOnce, At: fireAt.UTC().Format(time.RFC3339)},
		Message:    message,
		Status:     "active",
		NextFire:   &fireAt,
	}

	if err := pdb.CreateSubscription(ctx, sub); err != nil {
		if errors.Is(err, platformdb.ErrDuplicate) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("a schedule named %q already exists — cancel it first or choose a different name", name)), nil
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to create subscription: %v", err)), nil
	}

	if schedulerCb != nil {
		schedulerCb.Add(sub)
	}

	return fantasy.NewTextResponse(fmt.Sprintf("One-time schedule %q created. Fires at: %s", name, fireAt.Format("2006-01-02 15:04 MST"))), nil
}

// parseScheduleTime parses a time string as either a relative duration ("20m", "2h")
// or an absolute time ("2006-01-02T15:04:05") in the server timezone.
func parseScheduleTime(s string, tz *time.Location) (time.Time, error) {
	// Try Go duration first (e.g. "20m", "2h", "1h30m").
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}

	// Try RFC3339 (e.g. "2026-04-05T17:00:00Z" or "2026-04-05T17:00:00+05:00").
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try local time without timezone (e.g. "2026-04-05T17:00:00") — interpret in server tz.
	const localFormat = "2006-01-02T15:04:05"
	if t, err := time.ParseInLocation(localFormat, s, tz); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("expected a duration (e.g. '20m', '2h') or a datetime (e.g. '2026-04-05T17:00:00')")
}

func handleCancelSchedule(ctx context.Context, pdb *platformdb.DB, schedulerCb ScheduleCallback, name string) (fantasy.ToolResponse, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return fantasy.NewTextErrorResponse("name is required"), nil
	}

	callerID := callerIDFromContext(ctx)
	if callerID == "" {
		return fantasy.NewTextErrorResponse("no caller instance context"), nil
	}

	// Look up the subscription to get its ID for the scheduler.
	sub, err := pdb.GetSubscriptionByName(ctx, callerID, name)
	if err != nil {
		if errors.Is(err, platformdb.ErrNotFound) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("no schedule named %q found", name)), nil
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to look up schedule: %v", err)), nil
	}

	if err := pdb.DeleteSubscription(ctx, sub.ID); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to cancel schedule: %v", err)), nil
	}

	if schedulerCb != nil {
		schedulerCb.Remove(sub.ID)
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Schedule %q cancelled.", name)), nil
}

func handleListSchedules(ctx context.Context, pdb *platformdb.DB) (fantasy.ToolResponse, error) {
	callerID := callerIDFromContext(ctx)
	if callerID == "" {
		return fantasy.NewTextErrorResponse("no caller instance context"), nil
	}

	subs, err := pdb.ListSubscriptionsByInstance(ctx, callerID)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to list schedules: %v", err)), nil
	}

	if len(subs) == 0 {
		return fantasy.NewTextResponse("No active schedules."), nil
	}

	var b strings.Builder
	b.WriteString("| Name | Type | Schedule | Status | Next Fire | Fires | Errors |\n")
	b.WriteString("|------|------|----------|--------|-----------|-------|--------|\n")
	for _, s := range subs {
		nextFire := "—"
		if s.NextFire != nil {
			nextFire = s.NextFire.Format("2006-01-02 15:04")
		}
		schedule := s.Trigger.Expr
		if s.Trigger.Type == triggerTypeOnce {
			schedule = s.Trigger.At
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s | %s | %d | %d |\n",
			s.Name, s.Trigger.Type, schedule, s.Status, nextFire, s.FireCount, s.ErrorCount)
	}

	return fantasy.NewTextResponse(b.String()), nil
}
