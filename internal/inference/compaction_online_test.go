//go:build online

package inference

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openrouter"

	"github.com/joho/godotenv"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// testLM creates a real language model from .env for online tests.
// Returns nil and skips the test if credentials are not available.
//
// The model can be overridden with EVAL_MODEL env var, allowing parallel
// runs with different models:
//
//	EVAL_MODEL=x-ai/grok-4.1-fast go test -tags=online -run TestLoCoMo_Full ...
//	EVAL_MODEL=anthropic/claude-sonnet-4 go test -tags=online -run TestLoCoMo_Full ...
func testLM(t *testing.T) fantasy.LanguageModel {
	t.Helper()

	// Load .env from project root (walk up from test dir).
	for _, rel := range []string{"../../.env", "../../../.env"} {
		if abs, err := filepath.Abs(rel); err == nil {
			godotenv.Load(abs)
		}
	}

	provider := os.Getenv("HIRO_PROVIDER")
	apiKey := os.Getenv("HIRO_API_KEY")
	model := os.Getenv("HIRO_MODEL")

	// EVAL_MODEL overrides HIRO_MODEL, allowing parallel eval runs.
	if em := os.Getenv("EVAL_MODEL"); em != "" {
		model = em
	}

	if apiKey == "" {
		t.Skip("HIRO_API_KEY not set — skipping online test")
	}
	if provider == "" {
		provider = "openrouter"
	}
	if model == "" {
		t.Skip("HIRO_MODEL not set — skipping online test")
	}

	t.Logf("Using model: %s (provider: %s)", model, provider)

	// For now, only openrouter is supported in the online test.
	// This avoids importing the agent package (which imports inference → cycle).
	if provider != "openrouter" {
		t.Skipf("online test only supports openrouter provider, got %q", provider)
	}

	p, err := openrouter.New(openrouter.WithAPIKey(apiKey))
	if err != nil {
		t.Fatalf("creating openrouter provider: %v", err)
	}
	lm, err := p.LanguageModel(context.Background(), model)
	if err != nil {
		t.Fatalf("creating language model: %v", err)
	}
	return lm
}

// testModelName returns the model name for use in output file paths.
func testModelName(t *testing.T) string {
	t.Helper()
	for _, rel := range []string{"../../.env", "../../../.env"} {
		if abs, err := filepath.Abs(rel); err == nil {
			godotenv.Load(abs)
		}
	}
	model := os.Getenv("EVAL_MODEL")
	if model == "" {
		model = os.Getenv("HIRO_MODEL")
	}
	if model == "" {
		model = "unknown"
	}
	// Sanitize for filesystem: replace / with _
	return strings.ReplaceAll(model, "/", "_")
}

// --- LLM-as-judge scoring ---

// judgeGrade represents the judge's assessment of an answer.
type judgeGrade int

const (
	gradeIncorrect judgeGrade = iota // 0 — wrong, hallucinated, or missing
	gradePartial                     // 1 — right direction, missing key details
	gradeCorrect                     // 2 — captures the essential fact
)

func (g judgeGrade) Score() float64 {
	switch g {
	case gradeCorrect:
		return 1.0
	case gradePartial:
		return 0.5
	default:
		return 0.0
	}
}

func (g judgeGrade) String() string {
	switch g {
	case gradeCorrect:
		return "CORRECT"
	case gradePartial:
		return "PARTIAL"
	default:
		return "INCORRECT"
	}
}

// judgeResult holds the grade and the judge's explanation.
type judgeResult struct {
	Grade  judgeGrade
	Reason string
	Err    bool // true when the grade is due to a judge failure, not a real assessment
}

const judgePrompt = `Grade the answer against the reference. Reply with CORRECT, PARTIAL, or INCORRECT on the first line, then one sentence explaining why.

CORRECT: captures the key fact accurately
PARTIAL: right direction but missing important details or imprecise
INCORRECT: wrong, fabricated, or not addressed

Question: %s
Reference: %s
Answer: %s

Grade:`

// judgeAnswer uses the LLM to grade a model's answer against a reference.
// For adversarial questions (where the correct response is "unanswerable"),
// pass reference="unanswerable" — the judge handles refusal detection.
func judgeAnswer(ctx context.Context, lm fantasy.LanguageModel, question, reference, answer string) judgeResult {
	prompt := fmt.Sprintf(judgePrompt, question, reference, answer)

	resp, err := lm.Generate(ctx, fantasy.Call{
		Prompt: fantasy.Prompt{fantasy.NewUserMessage(prompt)},
	})
	if err != nil {
		return judgeResult{Grade: gradeIncorrect, Reason: "judge error: " + err.Error(), Err: true}
	}

	text := strings.TrimSpace(resp.Content.Text())

	// Parse grade from first line.
	firstLine := text
	reason := ""
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		firstLine = text[:idx]
		reason = strings.TrimSpace(text[idx+1:])
	}
	firstLine = strings.TrimSpace(firstLine)

	// Sometimes the model outputs "CORRECT:" or "CORRECT." — normalize.
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return judgeResult{Grade: gradeIncorrect, Reason: "judge returned empty response"}
	}
	rawFirstWord := fields[0]
	firstWord := strings.ToUpper(strings.TrimRight(rawFirstWord, ":.,-"))

	var grade judgeGrade
	switch firstWord {
	case "CORRECT":
		grade = gradeCorrect
	case "PARTIAL":
		grade = gradePartial
	default:
		grade = gradeIncorrect
	}

	// If reason wasn't on a separate line, use everything after the original first word.
	if reason == "" && len(firstLine) > len(rawFirstWord) {
		reason = strings.TrimSpace(firstLine[len(rawFirstWord):])
		reason = strings.TrimLeft(reason, ":.,-— ")
	}

	return judgeResult{Grade: grade, Reason: reason}
}

// TestOnline_LeafCompaction verifies that the full compaction pipeline produces
// meaningful summaries from a real LLM. We fill a session with realistic
// conversation messages, trigger a leaf pass, and verify the summary captures
// the key details.
func TestOnline_LeafCompaction(t *testing.T) {
	lm := testLM(t)
	pdb := openTestDB(t)
	createTestSession(t, pdb, "online-1")

	// Seed conversation with realistic multi-turn content about building a
	// web server. Each message pair adds context that the summary should capture.
	conversation := []struct {
		role, content string
	}{
		{"user", "I want to build a REST API in Go using the chi router. The API should manage a book collection with CRUD operations."},
		{"assistant", "I'll help you build that. Let's start with the project structure:\n\n```\ncmd/server/main.go\ninternal/api/routes.go\ninternal/api/books.go\ninternal/store/memory.go\n```\n\nFirst, let's set up the main server with chi."},
		{"user", "Looks good. Can you implement the Book struct and the in-memory store?"},
		{"assistant", "Here's the Book model and store:\n\n```go\ntype Book struct {\n\tID     string `json:\"id\"`\n\tTitle  string `json:\"title\"`\n\tAuthor string `json:\"author\"`\n\tISBN   string `json:\"isbn\"`\n}\n\ntype MemoryStore struct {\n\tmu    sync.RWMutex\n\tbooks map[string]Book\n}\n```\n\nI've implemented Get, List, Create, Update, and Delete methods on the store."},
		{"user", "Now let's add the HTTP handlers. I want proper error handling with JSON error responses."},
		{"assistant", "I've created the handlers with consistent JSON error responses:\n\n- GET /api/books - lists all books\n- GET /api/books/{id} - get a single book\n- POST /api/books - create a book (validates required fields)\n- PUT /api/books/{id} - update a book\n- DELETE /api/books/{id} - delete a book\n\nEach handler returns `{\"error\": \"message\"}` on failure with appropriate HTTP status codes (400, 404, 500)."},
		{"user", "Let's add pagination to the list endpoint. I want limit/offset query params with a default limit of 20."},
		{"assistant", "Done. The GET /api/books endpoint now accepts:\n- `limit` (default 20, max 100)\n- `offset` (default 0)\n\nResponse includes pagination metadata:\n```json\n{\"books\": [...], \"total\": 42, \"limit\": 20, \"offset\": 0}\n```\n\nI also added input validation — negative values are rejected with a 400 error."},
		{"user", "Great. Now add middleware for request logging and a 30-second timeout."},
		{"assistant", "Added two middleware to the chi chain:\n\n1. **RequestLogger** — logs method, path, status code, and duration for every request using slog\n2. **Timeout** — wraps each request in a 30s context deadline using chi's middleware.Timeout\n\nThe middleware stack in main.go:\n```go\nr.Use(middleware.Timeout(30 * time.Second))\nr.Use(RequestLogger)\n```"},
	}

	for _, msg := range conversation {
		tokens := EstimateTokens(msg.content)
		appendMsg(t, pdb, "online-1", msg.role, msg.content, tokens)
	}

	// Use a config that will trigger compaction on this conversation.
	cfg := CompactionConfig{
		ContextWindow:        200_000,
		SoftThreshold:        0.60,
		HardThreshold:        0.85,
		TokenBudget:          180_000,
		FreshTailCount:       2, // protect only last 2 messages
		LeafChunkTokens:      100,
		LeafTargetTokens:     500,
		CondenseTargetTokens: 800,
		LeafMinFanout:        3,
		CondenseMinFanout:    4,
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	summarizer := &lmSummarizer{lm: lm}
	compactor := NewCompactor(pdb, "online-1", summarizer, cfg, logger)

	// Run a leaf pass directly — we have enough messages outside the tail.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := compactor.leafPass(ctx)
	if err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	// Verify a summary was created.
	items, err := pdb.GetContextItems("online-1")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}

	var summaryItem *platformdb.ContextItem
	for i, item := range items {
		if item.ItemType == "summary" {
			summaryItem = &items[i]
			break
		}
	}
	if summaryItem == nil {
		t.Fatal("expected a summary context item after leaf compaction")
	}

	sum, err := pdb.GetSummary(*summaryItem.SummaryID)
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}

	t.Logf("Summary (depth=%d, tokens=%d, source_tokens=%d):\n%s", sum.Depth, sum.Tokens, sum.SourceTokens, sum.Content)

	// The summary should be meaningfully shorter than the source material.
	// With very short source messages the LLM may expand, so we only check
	// when source is substantial enough for compression to apply.
	if sum.SourceTokens > 500 && sum.Tokens >= sum.SourceTokens {
		t.Errorf("summary (%d tokens) should be shorter than source (%d tokens)", sum.Tokens, sum.SourceTokens)
	}
	t.Logf("Compression ratio: %d → %d tokens (%.1fx)", sum.SourceTokens, sum.Tokens, float64(sum.SourceTokens)/float64(sum.Tokens))

	// The summary should mention key details from the compacted messages
	// (earliest messages outside the fresh tail, not the full conversation).
	keywords := []string{"book", "chi", "API"}
	for _, kw := range keywords {
		if !containsCI(sum.Content, kw) {
			t.Errorf("summary missing expected keyword %q", kw)
		}
	}
}

// TestOnline_FullCompactionPipeline exercises the full CompactIfNeeded path
// with a real LLM: leaf pass, then condensation when enough summaries exist.
func TestOnline_FullCompactionPipeline(t *testing.T) {
	lm := testLM(t)
	pdb := openTestDB(t)
	createTestSession(t, pdb, "online-2")

	// Generate enough conversation to trigger multiple leaf passes followed
	// by condensation. We simulate 5 "phases" of conversation, each about
	// a different topic, to give the LLM meaningful material to condense.
	phases := []struct {
		topic    string
		messages []string
	}{
		{
			"database setup",
			[]string{
				"Let's add a PostgreSQL database to the book API instead of the in-memory store.",
				"I'll use pgx as the driver. Here's the schema:\n\nCREATE TABLE books (\n  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),\n  title TEXT NOT NULL,\n  author TEXT NOT NULL,\n  isbn TEXT UNIQUE\n);\n\nAnd the connection pool setup with pgxpool.",
				"Can you add a migration system?",
				"Done. Using golang-migrate with embed for SQL files. Migrations run on startup. Added migration 001_create_books.up.sql and .down.sql.",
			},
		},
		{
			"authentication",
			[]string{
				"Now let's add JWT authentication. Users should register and login.",
				"Added auth middleware and two new endpoints:\n\nPOST /api/auth/register - creates user with bcrypt-hashed password\nPOST /api/auth/login - returns JWT token (24h expiry)\n\nAll /api/books routes now require Authorization: Bearer <token>.",
				"Add role-based access. Only admins can delete books.",
				"Implemented roles (user, admin). DELETE /api/books/{id} now checks for admin role in JWT claims. Non-admins get 403 Forbidden.",
			},
		},
		{
			"search feature",
			[]string{
				"Let's add full-text search using PostgreSQL tsvector.",
				"Added a search endpoint GET /api/books/search?q=... using ts_rank and plainto_tsquery. The books table now has a tsvector column updated via trigger. Added GIN index for performance.",
				"Can we add fuzzy matching too?",
				"Added pg_trgm extension for fuzzy search. The search endpoint now combines FTS results with trigram similarity, with FTS weighted higher. Results are deduplicated and sorted by combined score.",
			},
		},
		{
			"testing",
			[]string{
				"We need integration tests for the API endpoints.",
				"Created tests using httptest and a test database. Each test runs in a transaction that rolls back. Covers: CRUD operations, pagination edge cases, auth flow, role enforcement, and search ranking.",
				"Add benchmarks for the search endpoint.",
				"Added BenchmarkSearch with 10K books. Results: simple FTS ~2ms, fuzzy ~8ms, combined ~12ms. The GIN index is doing its job — without it, combined search was ~200ms.",
			},
		},
		{
			"deployment",
			[]string{
				"Let's containerize this with Docker and set up CI.",
				"Created multi-stage Dockerfile (builder + alpine runtime). Added docker-compose.yml with the API and PostgreSQL services. Health check hits /api/health.",
				"Add GitHub Actions CI.",
				"Added .github/workflows/ci.yml: lint (golangci-lint), test (with PostgreSQL service container), build, and docker push to GHCR on main branch. Tests run with -race flag.",
			},
		},
	}

	for _, phase := range phases {
		for i, content := range phase.messages {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			tokens := EstimateTokens(content)
			appendMsg(t, pdb, "online-2", role, content, tokens)
		}
	}

	// Config designed to compact this conversation aggressively.
	// SoftThreshold at 0.01 ensures estimated tokens (which are small for
	// these short messages) still exceed the soft limit.
	cfg := CompactionConfig{
		ContextWindow:        1_000,
		SoftThreshold:        0.01, // 10 tokens — any content triggers compaction
		HardThreshold:        0.85,
		TokenBudget:          900,
		FreshTailCount:       2,
		LeafChunkTokens:      100,
		LeafTargetTokens:     400,
		CondenseTargetTokens: 600,
		LeafMinFanout:        3,
		CondenseMinFanout:    3,
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	summarizer := &lmSummarizer{lm: lm}
	compactor := NewCompactor(pdb, "online-2", summarizer, cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Simulate real trigger: pass estimated tokens as lastInputTokens
	// (since we don't have a real API call, just use a value above soft).
	estimated, err := pdb.ContextTokenCount("online-2")
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}
	t.Logf("Pre-compaction estimated tokens: %d", estimated)

	result, err := compactor.CompactIfNeeded(ctx, int64(estimated))
	if err != nil {
		t.Fatalf("CompactIfNeeded: %v", err)
	}

	// Check what we ended up with.
	items, err := pdb.GetContextItems("online-2")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}

	msgCount, sumCount := 0, 0
	for _, item := range items {
		switch item.ItemType {
		case "message":
			msgCount++
		case "summary":
			sumCount++
		}
	}

	postTokens, err := pdb.ContextTokenCount("online-2")
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}

	t.Logf("Post-compaction: %d messages, %d summaries, %d estimated tokens (was %d)",
		msgCount, sumCount, postTokens, estimated)
	t.Logf("Hard threshold exceeded: %v", result.HardThresholdExceeded)

	if sumCount == 0 {
		t.Error("expected at least one summary after compaction")
	}

	// Post-compaction token count may actually increase when summaries are
	// longer than the short source messages they replace. What matters is
	// that the pipeline ran and produced summaries — the compression ratio
	// is tested with longer messages in TestOnline_LeafCompaction.
	t.Logf("Token change: %d → %d", estimated, postTokens)

	// Check the max depth — should have condensation if enough leaf summaries were created.
	maxDepth, err := pdb.MaxSummaryDepth("online-2")
	if err != nil {
		t.Fatalf("MaxSummaryDepth: %v", err)
	}
	t.Logf("Max summary depth: %d", maxDepth)

	// Print all summaries for inspection.
	for _, item := range items {
		if item.ItemType != "summary" || item.SummaryID == nil {
			continue
		}
		sum, err := pdb.GetSummary(*item.SummaryID)
		if err != nil {
			t.Logf("GetSummary %s: %v", *item.SummaryID, err)
			continue
		}
		t.Logf("\n--- Summary %s (depth=%d, kind=%s, tokens=%d) ---\n%s",
			sum.ID, sum.Depth, sum.Kind, sum.Tokens, sum.Content)
	}

	// Verify the summaries are actually useful — the condensed overview
	// should mention multiple phases of the project.
	if maxDepth >= 1 {
		// Find the highest-depth summary.
		for _, item := range items {
			if item.ItemType != "summary" || item.SummaryID == nil {
				continue
			}
			sum, _ := pdb.GetSummary(*item.SummaryID)
			if sum.Depth == maxDepth {
				// A good condensed summary should reference multiple topics.
				topics := []string{"database", "auth", "search"}
				found := 0
				for _, topic := range topics {
					if containsCI(sum.Content, topic) {
						found++
					}
				}
				if found < 2 {
					t.Errorf("highest-depth summary should reference at least 2 of %v, found %d", topics, found)
				}
				break
			}
		}
	}
}

// TestOnline_EscalationToAggressive verifies that when a normal summary is too
// long, the escalation to aggressive mode produces a shorter result.
func TestOnline_EscalationToAggressive(t *testing.T) {
	lm := testLM(t)
	pdb := openTestDB(t)
	createTestSession(t, pdb, "online-3")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	summarizer := &lmSummarizer{lm: lm}

	cfg := DefaultCompactionConfig()
	// Set a very tight target to force escalation.
	cfg.LeafTargetTokens = 50

	compactor := NewCompactor(pdb, "online-3", summarizer, cfg, logger)

	// A substantial input that a normal summary won't compress to 50 tokens.
	input := `[10:00:00] user: I need to refactor our authentication system. Currently we store sessions in memory which doesn't work with multiple server instances.

[10:01:00] assistant: I'll help migrate to Redis-backed sessions. Here's the plan:
1. Add redis client dependency
2. Create a SessionStore interface
3. Implement RedisSessionStore
4. Update middleware to use the interface
5. Add session expiry configuration

[10:05:00] user: Sounds good, but we also need to handle the migration of existing sessions.

[10:06:00] assistant: Good point. I'll add a migration strategy:
- On first request, check memory store for existing session
- If found, write it to Redis and delete from memory
- Set a migration deadline (7 days) after which memory store is removed
- Add metrics to track migration progress

[10:10:00] user: Let's also add rate limiting per session.

[10:11:00] assistant: Added sliding window rate limiting using Redis sorted sets. Configuration:
- 100 requests per minute per session
- 429 Too Many Requests response with Retry-After header
- Rate limit headers on every response (X-RateLimit-Limit, X-RateLimit-Remaining)`

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	summary, err := compactor.summarizeWithEscalation(ctx, 0, input, "")
	if err != nil {
		t.Fatalf("summarizeWithEscalation: %v", err)
	}

	summaryTokens := EstimateTokens(summary)
	inputTokens := EstimateTokens(input)

	t.Logf("Input tokens: %d, Summary tokens: %d, Target: %d", inputTokens, summaryTokens, cfg.LeafTargetTokens)
	t.Logf("Summary:\n%s", summary)

	// The summary should be substantially shorter than the input,
	// even if it didn't hit the aggressive 50-token target.
	if summaryTokens >= inputTokens/2 {
		t.Errorf("summary (%d tokens) should be less than half of input (%d tokens)", summaryTokens, inputTokens)
	}

	// Should mention the key decisions.
	if !containsCI(summary, "Redis") && !containsCI(summary, "redis") {
		t.Error("summary should mention Redis (the core technology decision)")
	}
}

func containsCI(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
