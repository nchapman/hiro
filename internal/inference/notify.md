Send a message to the user's primary conversation. Use this in triggered sessions (scheduled tasks, webhooks) to surface important results. If you don't call Notify, the scheduled run stays silent.

Be selective — only notify when there's something worth the user's attention. A health check that finds no issues should stay quiet. A health check that finds a problem should notify immediately.
