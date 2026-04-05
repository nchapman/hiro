Schedule a one-time task at a specific time. The task fires once and is automatically cleaned up afterward. Like ScheduleRecurring, it runs in an isolated session and you can use Notify to surface results.

The `at` parameter accepts either:
- An absolute time: `2026-04-05T17:00:00` (server timezone)
- A relative duration: `20m`, `2h`, `1h30m`, `24h`

Relative durations are resolved to an absolute time when you create the schedule.

Examples:
- `at: "30m"` — fire in 30 minutes
- `at: "2h"` — fire in 2 hours
- `at: "2026-04-06T09:00:00"` — fire at 9am on April 6th
