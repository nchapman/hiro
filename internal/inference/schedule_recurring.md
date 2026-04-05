Schedule a recurring task using a cron expression. The task fires on the schedule and runs in an isolated session — your conversation history for each schedule is separate from the user's chat, so you can build context over time (e.g. "yesterday's report showed X, today it changed to Y").

Use the Notify tool within the triggered session to surface results to the user's primary conversation. If you don't call Notify, the scheduled run is silent.

Cron expression format (5 fields):
  minute hour day-of-month month day-of-week

Examples:
- `0 9 * * *`     — every day at 9am
- `0 9 * * 1-5`   — weekdays at 9am
- `*/10 * * * *`   — every 10 minutes
- `0 0 * * 0`     — every Sunday at midnight
- `30 8 1 * *`    — 8:30am on the 1st of each month

All times use the server's configured timezone.

The name must be unique within your instance and is used to identify the schedule for cancellation or listing.
