//go:build online

package inference

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

// codeConversation represents a multi-phase coding session with QA pairs
// designed to test preservation of technical details through compaction.
type codeConversation struct {
	Phases []codePhase
	QA     []codeQA
}

type codePhase struct {
	Topic    string
	Messages []codeMessage
}

type codeMessage struct {
	Role    string
	Content string
}

type codeQA struct {
	Question string
	Answer   string
	Category string // file_path, command, decision, error, config, architecture
}

// buildCodeConversations returns synthetic coding conversations with ground-truth
// QA pairs. Each conversation simulates a realistic multi-turn engineering session
// where the user and assistant work through a project together.
func buildCodeConversations() []codeConversation {
	return []codeConversation{
		backendAPIConversation(),
		debuggingConversation(),
	}
}

func backendAPIConversation() codeConversation {
	return codeConversation{
		Phases: []codePhase{
			{
				Topic: "project setup",
				Messages: []codeMessage{
					{"user", "I want to build a REST API in Go for managing a task queue. We'll use PostgreSQL for persistence and Redis for the job queue."},
					{"assistant", "Great choices. Here's the project structure I'd recommend:\n\n```\ncmd/server/main.go\ninternal/api/routes.go\ninternal/api/handlers.go\ninternal/store/postgres.go\ninternal/queue/redis.go\ninternal/models/task.go\n```\n\nI'll use chi v5 for routing, pgx v5 for PostgreSQL, and go-redis v9 for the queue. Let me start with the Task model."},
					{"user", "Sounds good. Make sure the Task model has an ID, title, priority (1-5), status, created_at, and assigned_to fields."},
					{"assistant", "Here's the Task model in `internal/models/task.go`:\n\n```go\ntype Task struct {\n\tID         uuid.UUID  `json:\"id\" db:\"id\"`\n\tTitle      string     `json:\"title\" db:\"title\"`\n\tPriority   int        `json:\"priority\" db:\"priority\"` // 1-5\n\tStatus     string     `json:\"status\" db:\"status\"`     // pending, running, completed, failed\n\tCreatedAt  time.Time  `json:\"created_at\" db:\"created_at\"`\n\tAssignedTo *string    `json:\"assigned_to\" db:\"assigned_to\"`\n}\n```\n\nI've created the migration at `migrations/001_create_tasks.sql`:\n```sql\nCREATE TABLE tasks (\n  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),\n  title TEXT NOT NULL,\n  priority INTEGER NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5),\n  status TEXT NOT NULL DEFAULT 'pending',\n  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),\n  assigned_to TEXT\n);\nCREATE INDEX idx_tasks_status ON tasks(status);\nCREATE INDEX idx_tasks_priority ON tasks(priority DESC);\n```"},
				},
			},
			{
				Topic: "API endpoints",
				Messages: []codeMessage{
					{"user", "Now add the CRUD endpoints. I want the list endpoint to support filtering by status and sorting by priority."},
					{"assistant", "Done. The API routes in `internal/api/routes.go`:\n\n```\nGET    /api/tasks         - list tasks (query: ?status=pending&sort=priority)\nGET    /api/tasks/{id}     - get single task\nPOST   /api/tasks          - create task\nPUT    /api/tasks/{id}     - update task\nDELETE /api/tasks/{id}     - delete task\nPOST   /api/tasks/{id}/run - enqueue task for execution\n```\n\nThe list endpoint parses `status` and `sort` query params. Sort supports `priority` (desc), `created_at` (desc), and `title` (asc). Default is `created_at` desc.\n\nI also added request validation — POST/PUT require `title` and validate `priority` is 1-5. Returns 400 with `{\"error\": \"message\"}` on validation failure."},
					{"user", "Add pagination with cursor-based pagination instead of offset. Use the task ID as cursor."},
					{"assistant", "Updated the list endpoint to use cursor-based pagination:\n\n```\nGET /api/tasks?cursor=<uuid>&limit=20&status=pending\n```\n\nThe cursor is the last task ID from the previous page. I'm using `WHERE id > $cursor ORDER BY id` for stable pagination. Default limit is 20, max is 100.\n\nResponse format:\n```json\n{\n  \"tasks\": [...],\n  \"next_cursor\": \"uuid-of-last-task\",\n  \"has_more\": true\n}\n```"},
				},
			},
			{
				Topic: "Redis queue integration",
				Messages: []codeMessage{
					{"user", "Set up the Redis job queue. Tasks should be enqueued when hitting POST /api/tasks/{id}/run and processed by a worker pool."},
					{"assistant", "Implemented the queue in `internal/queue/redis.go`:\n\n- Queue name: `tasks:pending` (Redis sorted set, scored by priority)\n- Worker pool: 8 concurrent workers (configurable via `WORKER_POOL_SIZE`)\n- Processing timeout: 30 seconds per task\n- Retry policy: 3 attempts with exponential backoff (1s, 4s, 16s)\n- Dead letter queue: `tasks:dead` after exhausting retries\n\nThe worker calls `processTask()` which updates status to `running`, executes the task, then sets `completed` or `failed`. I used `BRPOPLPUSH` for reliable queue consumption with an in-progress list at `tasks:processing`.\n\nAdded health check: `GET /api/health` returns queue depth and worker count."},
					{"user", "I'm worried about task processing taking too long. Can we add a watchdog that moves stuck tasks back to the queue?"},
					{"assistant", "Added a watchdog goroutine in `internal/queue/watchdog.go`:\n\n- Runs every 60 seconds\n- Checks `tasks:processing` for entries older than 2x the processing timeout (60s)\n- Moves stuck tasks back to `tasks:pending` with `RPOPLPUSH`\n- Increments a `retry_count` field on the task\n- Logs a warning: `\"watchdog recovered stuck task\"` with task ID and duration\n- After 3 recoveries, moves to dead letter queue\n\nThe watchdog uses a distributed lock (`tasks:watchdog:lock`) with 30s TTL to prevent multiple instances from running concurrently."},
				},
			},
			{
				Topic: "testing and deployment",
				Messages: []codeMessage{
					{"user", "Write integration tests for the queue. I want to verify the retry behavior and dead letter queue."},
					{"assistant", "Created `internal/queue/redis_test.go` with integration tests using testcontainers:\n\n1. `TestEnqueueDequeue` - basic enqueue/dequeue flow\n2. `TestPriorityOrdering` - higher priority tasks dequeued first\n3. `TestRetryOnFailure` - failed task retried 3 times with backoff\n4. `TestDeadLetterQueue` - task moved to DLQ after 3 failures\n5. `TestWatchdogRecovery` - stuck task recovered by watchdog\n6. `TestConcurrentWorkers` - 8 workers process tasks in parallel\n\nEach test spins up a Redis container via testcontainers-go. I'm using `github.com/testcontainers/testcontainers-go` v0.28.0.\n\nBenchmark results for enqueue/dequeue:\n- Single task: 0.3ms\n- 1000 tasks with 8 workers: 450ms (2222 tasks/sec)"},
					{"user", "Great. Now set up the Dockerfile and docker-compose for deployment."},
					{"assistant", "Created:\n\n**Dockerfile** (multi-stage):\n```dockerfile\n# Build stage\nFROM golang:1.22-alpine AS builder\nWORKDIR /app\nCOPY go.mod go.sum ./\nRUN go mod download\nCOPY . .\nRUN CGO_ENABLED=0 go build -o /server ./cmd/server\n\n# Runtime\nFROM alpine:3.19\nCOPY --from=builder /server /server\nEXPOSE 8080\nENTRYPOINT [\"/server\"]\n```\n\n**docker-compose.yml**:\n- `api`: the Go server on port 8080\n- `postgres`: PostgreSQL 16 with persistent volume\n- `redis`: Redis 7.2 with `maxmemory 256mb` and `maxmemory-policy allkeys-lru`\n\nEnvironment variables:\n- `DATABASE_URL`: `postgres://taskq:taskq@postgres:5432/taskq?sslmode=disable`\n- `REDIS_URL`: `redis://redis:6379/0`\n- `WORKER_POOL_SIZE`: `8`\n- `PORT`: `8080`\n\nI also added a `Makefile` with targets: `build`, `test`, `docker-build`, `docker-up`, `migrate`."},
				},
			},
			{
				Topic: "performance issue",
				Messages: []codeMessage{
					{"user", "We're seeing slow response times on the list endpoint when there are 100K+ tasks. Can you investigate?"},
					{"assistant", "I ran `EXPLAIN ANALYZE` on the list query and found the issue. The query was doing a sequential scan because the `WHERE status = $1 ORDER BY priority DESC` didn't have a composite index.\n\nAdded migration `003_add_composite_index.sql`:\n```sql\nCREATE INDEX idx_tasks_status_priority ON tasks(status, priority DESC);\n```\n\nBefore: 850ms for 100K rows with status filter\nAfter: 2.3ms with the composite index\n\nI also added connection pooling config: `pgxpool.Config{MaxConns: 25, MinConns: 5}` in `internal/store/postgres.go`. The previous default of 4 max connections was causing contention under load."},
				},
			},
		},
		QA: []codeQA{
			// File paths
			{"Where is the Task model defined?", "internal/models/task.go", "file_path"},
			{"What file contains the Redis queue implementation?", "internal/queue/redis.go", "file_path"},
			{"Where is the watchdog implemented?", "internal/queue/watchdog.go", "file_path"},
			{"What is the migration file for the composite index?", "003_add_composite_index.sql", "file_path"},
			// Commands and configs
			{"What router is used?", "chi v5", "config"},
			{"What PostgreSQL driver is used?", "pgx v5", "config"},
			{"How many concurrent workers process tasks?", "8", "config"},
			{"What is the Redis max memory setting?", "256mb", "config"},
			{"What Go version is used in the Dockerfile?", "1.22", "config"},
			{"What PostgreSQL version is in docker-compose?", "16", "config"},
			// Decisions and architecture
			{"What type of pagination does the API use?", "cursor-based pagination using task ID", "decision"},
			{"What data structure is used for the Redis queue?", "sorted set scored by priority", "architecture"},
			{"What queue consumption pattern is used?", "BRPOPLPUSH with in-progress list", "architecture"},
			{"What is the retry policy for failed tasks?", "3 attempts with exponential backoff 1s 4s 16s", "decision"},
			{"What happens after a task exhausts its retries?", "moved to dead letter queue tasks:dead", "decision"},
			// Errors and performance
			{"What was causing slow response times on the list endpoint?", "sequential scan, missing composite index on status and priority", "error"},
			{"What was the query time before the index fix?", "850ms", "error"},
			{"What was the query time after the index fix?", "2.3ms", "error"},
			{"What was the max connections issue?", "default of 4 max connections causing contention, increased to 25", "config"},
			// Task model specifics
			{"What are the valid task statuses?", "pending running completed failed", "architecture"},
			{"What is the priority range for tasks?", "1 to 5", "config"},
			{"What is the processing timeout per task?", "30 seconds", "config"},
			{"How often does the watchdog run?", "every 60 seconds", "config"},
			{"What benchmark throughput was achieved?", "2222 tasks per second", "config"},
		},
	}
}

func debuggingConversation() codeConversation {
	return codeConversation{
		Phases: []codePhase{
			{
				Topic: "initial bug report",
				Messages: []codeMessage{
					{"user", "Users are reporting 502 errors on our production API at api.acme.com. It started about an hour ago. The service runs on Kubernetes with 3 replicas behind an nginx ingress."},
					{"assistant", "Let me investigate. I checked the pod logs:\n\n```\nkubectl logs -l app=acme-api --tail=100\n```\n\nI see repeated errors:\n```\nERROR 2024-03-15T14:23:01Z database/sql: connection pool exhausted (max=10, in-use=10, idle=0)\nERROR 2024-03-15T14:23:01Z handler: POST /api/orders failed: context deadline exceeded\n```\n\nThe connection pool is maxed out. Let me check the database side."},
					{"user", "What's happening on the database?"},
					{"assistant", "Ran `pg_stat_activity` on the RDS instance (db.r6g.xlarge):\n\n```sql\nSELECT state, count(*) FROM pg_stat_activity WHERE datname='acme' GROUP BY state;\n```\n\nResults:\n- active: 28\n- idle in transaction: 15\n- idle: 7\n\nThe 15 \"idle in transaction\" connections are the problem — they're holding connections without doing work. Looking at the queries:\n\n```sql\nSELECT pid, state, query, now()-state_change AS duration\nFROM pg_stat_activity\nWHERE state = 'idle in transaction' ORDER BY duration DESC;\n```\n\nThe longest-running one has been idle for 47 minutes. The query is:\n```sql\nSELECT * FROM orders WHERE user_id = $1 FOR UPDATE\n```\n\nThis is the `reserveOrder` function in `internal/orders/service.go` at line 142. It opens a transaction, acquires a row lock, then calls an external payment API. If the API is slow or times out, the transaction stays open."},
				},
			},
			{
				Topic: "root cause and fix",
				Messages: []codeMessage{
					{"user", "So the payment API is causing connection leaks? How do we fix this?"},
					{"assistant", "Yes, exactly. The root cause is holding a database transaction open while making an external HTTP call. The payment API (payments.stripe.com) has been intermittently slow — I checked the Stripe status page and they reported degraded performance starting at 13:15 UTC.\n\nHere's the fix in `internal/orders/service.go`:\n\n1. **Separate the transaction from the API call** — read order data, release the transaction, call Stripe, then use a new transaction to update:\n\n```go\n// Before (broken): transaction held across HTTP call\nfunc (s *Service) reserveOrder(ctx context.Context, orderID string) error {\n    tx, _ := s.db.BeginTx(ctx, nil)\n    defer tx.Rollback()\n    order, _ := tx.QueryRow(\"SELECT ... FOR UPDATE\", orderID)\n    result, _ := s.stripe.Charge(order.Amount) // <-- holds tx open!\n    tx.Exec(\"UPDATE orders SET status=$1 WHERE id=$2\", \"paid\", orderID)\n    return tx.Commit()\n}\n\n// After (fixed): release tx before HTTP call\nfunc (s *Service) reserveOrder(ctx context.Context, orderID string) error {\n    order, _ := s.getOrder(ctx, orderID) // no transaction needed for read\n    result, _ := s.stripe.Charge(order.Amount) // no tx held\n    return s.updateOrderStatus(ctx, orderID, \"paid\", result.ChargeID) // short tx\n}\n```\n\n2. **Add connection pool settings**: `SetConnMaxIdleTime(5 * time.Minute)` and `SetMaxOpenConns(25)` in `cmd/server/main.go`\n\n3. **Add transaction timeout**: `SET idle_in_transaction_session_timeout = '30s'` as a database-level safety net"},
					{"user", "Good analysis. Deploy the fix and let me know when it's stable."},
					{"assistant", "Deployed in three steps:\n\n1. **Immediate mitigation**: Killed the 15 idle-in-transaction connections:\n   ```sql\n   SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE state = 'idle in transaction' AND datname='acme';\n   ```\n   502 errors stopped within 30 seconds.\n\n2. **Deployed the code fix**: Built and pushed `acme-api:v2.14.1`:\n   ```\n   docker build -t acme-api:v2.14.1 .\n   kubectl set image deployment/acme-api api=acme-api:v2.14.1\n   ```\n   Rolling update completed in 90 seconds, zero downtime.\n\n3. **Added database safety net**:\n   ```sql\n   ALTER SYSTEM SET idle_in_transaction_session_timeout = '30s';\n   SELECT pg_reload_conf();\n   ```\n\nMonitoring for the last 15 minutes shows:\n- 0 errors (was ~50/min during incident)\n- p99 latency: 180ms (was timing out at 30s)\n- Active DB connections: 8-12 (was 50+)\n- No idle-in-transaction connections"},
				},
			},
			{
				Topic: "post-mortem items",
				Messages: []codeMessage{
					{"user", "Nice work. Add an alert so we catch this earlier next time."},
					{"assistant", "Added Prometheus alert in `deploy/alerts/database.yaml`:\n\n```yaml\n- alert: HighIdleInTransactionConnections\n  expr: pg_stat_activity_count{state=\"idle in transaction\"} > 5\n  for: 5m\n  labels:\n    severity: warning\n  annotations:\n    summary: \"{{ $value }} idle-in-transaction connections for 5+ minutes\"\n```\n\nAlso added a Grafana dashboard panel showing connection states over time.\n\nPost-mortem TODO items:\n- [ ] Audit all other transaction patterns for external calls (internal/payments/, internal/shipping/)\n- [ ] Add circuit breaker for Stripe API calls\n- [ ] Set up connection pool metrics in Prometheus\n- [ ] Review RDS instance size — db.r6g.xlarge has max 100 connections"},
				},
			},
		},
		QA: []codeQA{
			// Error specifics
			{"What error were users seeing?", "502 errors", "error"},
			{"What was the database error message?", "connection pool exhausted max 10 in-use 10 idle 0", "error"},
			{"What was the root cause of the 502 errors?", "holding database transaction open during external Stripe API call", "error"},
			{"How many idle in transaction connections were there?", "15", "error"},
			{"How long was the longest idle transaction running?", "47 minutes", "error"},
			// File paths and locations
			{"What file had the broken reserveOrder function?", "internal/orders/service.go", "file_path"},
			{"What line was the problematic code on?", "142", "file_path"},
			{"Where is the Prometheus alert defined?", "deploy/alerts/database.yaml", "file_path"},
			// Infrastructure
			{"What Kubernetes setup runs the API?", "3 replicas behind nginx ingress", "architecture"},
			{"What is the production API domain?", "api.acme.com", "config"},
			{"What RDS instance type is used?", "db.r6g.xlarge", "config"},
			{"What was the deployed version with the fix?", "v2.14.1", "config"},
			// Decisions and fixes
			{"What connection pool settings were added?", "SetConnMaxIdleTime 5 minutes and SetMaxOpenConns 25", "decision"},
			{"What database timeout was set as a safety net?", "idle_in_transaction_session_timeout 30s", "decision"},
			// Performance metrics
			{"What was the p99 latency during the incident?", "30 seconds timeout", "error"},
			{"What was the p99 latency after the fix?", "180ms", "config"},
			{"How many errors per minute during the incident?", "50", "error"},
			// SQL specifics
			{"What SQL query was holding the locks?", "SELECT * FROM orders WHERE user_id = $1 FOR UPDATE", "error"},
			// Post-mortem
			{"What alert threshold was set for idle in transaction connections?", "greater than 5 for 5 minutes", "decision"},
			{"What post-mortem item involves the Stripe API?", "add circuit breaker for Stripe API calls", "decision"},
		},
	}
}

// --- Code eval runner ---

type codeEvalSummary struct {
	ByCategory map[string][]float64
	Overall    []float64
}

func (s codeEvalSummary) Report() string {
	var b strings.Builder

	cats := make([]string, 0, len(s.ByCategory))
	for c := range s.ByCategory {
		cats = append(cats, c)
	}
	// Sort for stable output.
	for i := 0; i < len(cats); i++ {
		for j := i + 1; j < len(cats); j++ {
			if cats[i] > cats[j] {
				cats[i], cats[j] = cats[j], cats[i]
			}
		}
	}

	for _, c := range cats {
		scores := s.ByCategory[c]
		avg := mean(scores)
		fmt.Fprintf(&b, "  %s: %.1f%% F1 (%d questions)\n", c, avg*100, len(scores))
	}
	fmt.Fprintf(&b, "  Overall: %.1f%% F1 (%d questions)\n", mean(s.Overall)*100, len(s.Overall))
	return b.String()
}

func runCodeEval(t *testing.T, lm fantasy.LanguageModel, compact bool) codeEvalSummary {
	t.Helper()
	convos := buildCodeConversations()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	summary := codeEvalSummary{
		ByCategory: make(map[string][]float64),
	}

	for ci, conv := range convos {
		pdb := openTestDB(t)
		sessionID := fmt.Sprintf("code-eval-%d", ci)
		createTestSession(t, pdb, sessionID)

		// Ingest all messages.
		baseTime := time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)
		msgIdx := 0
		for _, phase := range conv.Phases {
			for _, msg := range phase.Messages {
				content := msg.Content
				tokens := EstimateTokens(content)
				msgID, err := pdb.AppendMessage(sessionID, msg.Role, content, "{}", tokens)
				if err != nil {
					t.Fatalf("AppendMessage: %v", err)
				}
				// Set realistic timestamps — each message 2 minutes apart.
				msgTime := baseTime.Add(time.Duration(msgIdx) * 2 * time.Minute)
				pdb.UpdateMessageTimestamp(msgID, msgTime)
				msgIdx++
			}
		}

		preTokens, _ := pdb.ContextTokenCount(sessionID)

		if compact {
			// Code conversations are shorter than LoCoMo — force compaction
			// with a tight window to stress-test fact preservation.
			window := max(preTokens*3/2, 1_000)
			cfg := compactionConfigForWindow(window)
			cfg.SoftThreshold = 0.01
			cfg.FreshTailCount = max(2, msgIdx/5) // protect ~20% of messages

			summarizer := &lmSummarizer{lm: lm}
			estimated := preTokens

			const maxRounds = 5
			for round := 0; round < maxRounds; round++ {
				compactor := NewCompactor(pdb, sessionID, summarizer, cfg, logger)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				_, err := compactor.CompactIfNeeded(ctx, int64(estimated))
				cancel()
				if err != nil {
					t.Logf("Conv %d round %d: compaction failed: %v", ci, round, err)
					break
				}

				newEstimated, _ := pdb.ContextTokenCount(sessionID)
				if newEstimated >= estimated {
					break
				}
				estimated = newEstimated
			}

			postTokens, _ := pdb.ContextTokenCount(sessionID)
			items, _ := pdb.GetContextItems(sessionID)
			msgCount, sumCount := 0, 0
			for _, item := range items {
				if item.ItemType == "message" {
					msgCount++
				} else {
					sumCount++
				}
			}
			maxDepth, _ := pdb.MaxSummaryDepth(sessionID)
			reduction := 0.0
			if preTokens > 0 {
				reduction = (1 - float64(postTokens)/float64(preTokens)) * 100
			}
			t.Logf("Conv %d: %d msgs → %d messages + %d summaries (depth %d), %d → %d est tokens (%.0f%% reduction)",
				ci, msgIdx, msgCount, sumCount, maxDepth, preTokens, postTokens, reduction)
		}

		// Assemble context.
		assembleCfg := DefaultCompactionConfig()
		assembled, err := Assemble(pdb, sessionID, assembleCfg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		var contextBuf strings.Builder
		for _, msg := range assembled.Messages {
			for _, part := range msg.Content {
				if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
					contextBuf.WriteString(tp.Text)
					contextBuf.WriteString("\n")
				}
			}
		}
		contextStr := contextBuf.String()

		// Score QA pairs.
		for qi, qa := range conv.QA {
			prompt := fmt.Sprintf(
				"Based on the following conversation history, answer the question. "+
					"Give a short, direct answer (a few words or a short phrase). "+
					"The conversation may contain summaries of earlier portions — "+
					"treat summarized content as authoritative. "+
					"If the question cannot be answered from the conversation, say \"unanswerable\".\n\n"+
					"Conversation:\n%s\n\nQuestion: %s\nAnswer:",
				contextStr, qa.Question,
			)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			resp, err := lm.Generate(ctx, fantasy.Call{
				Prompt: fantasy.Prompt{fantasy.NewUserMessage(prompt)},
			})
			cancel()

			answer := ""
			if err != nil {
				t.Logf("  [%d/%d] ERROR %q: %v", qi+1, len(conv.QA), qa.Question, err)
			} else {
				answer = strings.TrimSpace(resp.Content.Text())
			}

			score := f1Score(answer, qa.Answer)
			summary.ByCategory[qa.Category] = append(summary.ByCategory[qa.Category], score)
			summary.Overall = append(summary.Overall, score)

			marker := "✓"
			if score < 0.5 {
				marker = "✗"
			}
			t.Logf("  [%d/%d] %s F1=%.0f%% (%s) %q → got %q (expected %q)",
				qi+1, len(conv.QA), marker, score*100, qa.Category,
				qa.Question, truncateStr(answer, 80), truncateStr(qa.Answer, 80))
		}

		t.Logf("Conv %d done: %d QA pairs, running overall F1=%.1f%%",
			ci, len(conv.QA), mean(summary.Overall)*100)
	}

	return summary
}

// TestCodeEval_Baseline runs the code eval without compaction.
func TestCodeEval_Baseline(t *testing.T) {
	lm := testLM(t)
	summary := runCodeEval(t, lm, false)
	t.Logf("\n=== CODE BASELINE (no compaction) ===\n%s", summary.Report())
}

// TestCodeEval_Compacted runs the code eval after compaction.
func TestCodeEval_Compacted(t *testing.T) {
	lm := testLM(t)
	summary := runCodeEval(t, lm, true)
	t.Logf("\n=== CODE COMPACTED ===\n%s", summary.Report())
}
