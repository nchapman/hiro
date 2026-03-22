//go:build online

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
)

// loadEnv loads .env from the project root and returns provider, key, model.
// Skips the test if credentials are missing.
func loadEnv(t *testing.T) (ProviderType, string, string) {
	t.Helper()
	// Walk up to find .env
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			godotenv.Load(filepath.Join(dir, ".env"))
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	provider := os.Getenv("HIVE_PROVIDER")
	apiKey := os.Getenv("HIVE_API_KEY")
	model := os.Getenv("HIVE_MODEL")

	if apiKey == "" {
		t.Skip("HIVE_API_KEY not set — skipping online test")
	}
	if provider == "" {
		provider = "anthropic"
	}

	return ProviderType(provider), apiKey, model
}

// repoRoot walks up from cwd to find the directory containing go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
	return ""
}

func setupOnlineManager(t *testing.T) (*Manager, string) {
	t.Helper()
	provider, apiKey, model := loadEnv(t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mgr := NewManager(t.Context(), dir, Options{
		Provider:   provider,
		APIKey:     apiKey,
		Model:      model,
		WorkingDir: dir,
	}, logger)
	return mgr, dir
}

const onlineAgentMD = `---
name: online-test
model: ""
mode: persistent
---

You are a concise test agent. Always respond in one short sentence. Never use tools unless explicitly asked.`

const ephemeralAgentMD = `---
name: ephemeral-test
model: ""
mode: ephemeral
---

You are a concise test agent. Respond in one short sentence.`

func TestOnline_BasicChat(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SendMessage(ctx, id, "What is 2+2? Reply with just the number.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(resp, "4") {
		t.Errorf("expected response containing '4', got %q", resp)
	}
	t.Logf("Response: %s", resp)
}

func TestOnline_StreamingDelta(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var deltas []string
	resp, err := mgr.SendMessage(ctx, id, "Say hello.", func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if len(deltas) == 0 {
		t.Error("expected streaming deltas, got none")
	}
	if resp == "" {
		t.Error("expected non-empty response")
	}
	t.Logf("Got %d deltas, response: %s", len(deltas), resp)
}

func TestOnline_MultiTurn(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// First turn: establish a fact
	_, err = mgr.SendMessage(ctx, id, "Remember: the secret word is 'pineapple'. Just acknowledge.", nil)
	if err != nil {
		t.Fatalf("Turn 1: %v", err)
	}

	// Second turn: recall the fact
	resp, err := mgr.SendMessage(ctx, id, "What is the secret word?", nil)
	if err != nil {
		t.Fatalf("Turn 2: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "pineapple") {
		t.Errorf("expected 'pineapple' in response, got %q", resp)
	}
	t.Logf("Recall response: %s", resp)
}

func TestOnline_Memory(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	// Pre-write a memory file
	instDir := filepath.Join(dir, "instances", id)
	if err := config.WriteMemoryFile(instDir, "The user's favorite color is blue."); err != nil {
		t.Fatalf("WriteMemoryFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SendMessage(ctx, id, "What is my favorite color? Just say the color.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "blue") {
		t.Errorf("expected 'blue' from memory, got %q", resp)
	}
	t.Logf("Memory response: %s", resp)
}

func TestOnline_SpawnSubagent(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "ephemeral-test", ephemeralAgentMD)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	resp, err := mgr.SpawnSubagent(ctx, "ephemeral-test", "What is the capital of France? One word.", "")
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}

	if !strings.Contains(strings.ToLower(resp), "paris") {
		t.Errorf("expected 'Paris', got %q", resp)
	}
	t.Logf("Subagent response: %s", resp)
}

func TestOnline_ConversationHistory(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "online-test", onlineAgentMD)

	id, err := mgr.StartAgent(t.Context(), "online-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// Have a conversation with enough turns to verify history DB is working
	_, err = mgr.SendMessage(ctx, id, "I'm going to tell you three words. First word: elephant.", nil)
	if err != nil {
		t.Fatalf("Turn 1: %v", err)
	}

	_, err = mgr.SendMessage(ctx, id, "Second word: telescope.", nil)
	if err != nil {
		t.Fatalf("Turn 2: %v", err)
	}

	_, err = mgr.SendMessage(ctx, id, "Third word: saxophone.", nil)
	if err != nil {
		t.Fatalf("Turn 3: %v", err)
	}

	resp, err := mgr.SendMessage(ctx, id, "What were the three words I told you? List them.", nil)
	if err != nil {
		t.Fatalf("Turn 4: %v", err)
	}

	lower := strings.ToLower(resp)
	for _, word := range []string{"elephant", "telescope", "saxophone"} {
		if !strings.Contains(lower, word) {
			t.Errorf("expected %q in response, got %q", word, resp)
		}
	}
	t.Logf("History recall: %s", resp)

	// Verify history.db was created
	instDir := filepath.Join(dir, "instances", id)
	if _, err := os.Stat(filepath.Join(instDir, "history.db")); os.IsNotExist(err) {
		t.Error("history.db was not created for persistent agent")
	}
}

const todoAgentMD = `---
name: todo-test
model: ""
mode: persistent
---

You are a concise test agent. When asked to plan work, use the todos tool to create a task list. Keep responses short.`

func TestOnline_Todos(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "todo-test", todoAgentMD)

	id, err := mgr.StartAgent(t.Context(), "todo-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// Ask the agent to create a todo list — it should use the todos tool
	resp, err := mgr.SendMessage(ctx, id, "Create a todo list with exactly 3 tasks for building a REST API: design schema, implement endpoints, write tests. Use the todos tool now.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	t.Logf("Todo response: %s", resp)

	// Verify todos.yaml was created
	instDir := filepath.Join(dir, "instances", id)
	todos, err := config.ReadTodos(instDir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(todos) == 0 {
		t.Fatal("expected todos to be created, got none")
	}
	t.Logf("Created %d todos:", len(todos))
	for _, td := range todos {
		t.Logf("  [%s] %s", td.Status, td.Content)
	}
}

const memoryAgentMD = `---
name: memory-test
model: ""
mode: persistent
---

You are a concise test agent. When asked to save something to memory, use the memory_write tool immediately. Keep responses short.`

func TestOnline_MemoryWriteTool(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "memory-test", memoryAgentMD)

	id, err := mgr.StartAgent(t.Context(), "memory-test", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// Ask the agent to write a memory
	_, err = mgr.SendMessage(ctx, id, "Save this to memory: 'The project uses PostgreSQL 16'.", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Verify memory.md was created with the content
	instDir := filepath.Join(dir, "instances", id)
	content, err := config.ReadMemoryFile(instDir)
	if err != nil {
		t.Fatalf("ReadMemoryFile: %v", err)
	}
	if content == "" {
		t.Fatal("expected memory to be written, got empty")
	}
	if !strings.Contains(strings.ToLower(content), "postgresql") {
		t.Errorf("expected memory to contain 'postgresql', got %q", content)
	}
	t.Logf("Memory written: %s", content)
}

// TestOnline_CreateAgent verifies the coordinator's create-agent skill works end-to-end:
// a persistent agent creates a new agent definition on disk, starts it, and communicates with it.
func TestOnline_CreateAgent(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	// Set up a "creator" agent that has the create-agent skill
	creatorMD := `---
name: creator
model: ""
mode: persistent
---

You are a test agent. You can create and manage other agents.
Always follow instructions precisely. Keep responses short.`

	// Load the real create-agent skill from the repo
	root := repoRoot(t)
	createAgentSkill, err := os.ReadFile(filepath.Join(root, "agents", "coordinator", "skills", "create-agent.md"))
	if err != nil {
		t.Fatalf("reading create-agent skill: %v", err)
	}

	// Write the creator agent definition with the real skill
	writeAgentMD(t, dir, "creator", creatorMD)
	skillsDir := filepath.Join(dir, "agents", "creator", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "create-agent.md"), createAgentSkill, 0644); err != nil {
		t.Fatal(err)
	}

	id, err := mgr.StartAgent(t.Context(), "creator", "")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	// Ask the creator to build a new agent and spawn it.
	// The working directory is the workspace root, so relative paths like agents/greeter/agent.md
	// should resolve correctly against it.
	resp, err := mgr.SendMessage(ctx, id, `Create a new agent called "greeter". Write agents/greeter/agent.md with this exact content:

---
name: greeter
mode: ephemeral
model: ""
---

Always respond with exactly "HELLO WORLD" in all caps. Nothing else.

Then use spawn_agent with agent "greeter" and prompt "Say your greeting." and report back what it said.`, nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	t.Logf("Creator response: %s", resp)

	// Verify the agent definition was created on disk
	agentMDPath := filepath.Join(dir, "agents", "greeter", "agent.md")
	content, err := os.ReadFile(agentMDPath)
	if err != nil {
		t.Fatalf("expected agents/greeter/agent.md to exist: %v", err)
	}
	t.Logf("Created agent.md:\n%s", string(content))

	// The spawn result should contain the greeter's output
	if !strings.Contains(strings.ToUpper(resp), "HELLO WORLD") {
		t.Errorf("expected spawn result containing 'HELLO WORLD', got %q", resp)
	}
}

// TestOnline_CreateSkill verifies a running agent can have a skill added to it at runtime.
// A "builder" agent writes a skill file for a "responder" agent, then the responder uses
// the new skill on its next message.
func TestOnline_CreateSkill(t *testing.T) {
	mgr, dir := setupOnlineManager(t)
	defer mgr.Shutdown()

	// Load the real create-skill skill from the repo
	root := repoRoot(t)
	createSkillContent, err := os.ReadFile(filepath.Join(root, "agents", "coordinator", "skills", "create-skill.md"))
	if err != nil {
		t.Fatalf("reading create-skill: %v", err)
	}

	// Set up a "builder" agent that has the create-skill skill
	builderMD := `---
name: builder
model: ""
mode: persistent
---

You are a test agent that adds skills to other agents.
Always follow instructions precisely. Keep responses short.`

	writeAgentMD(t, dir, "builder", builderMD)
	skillsDir := filepath.Join(dir, "agents", "builder", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "create-skill.md"), createSkillContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Set up a "responder" agent with no skills yet
	responderMD := `---
name: responder
model: ""
mode: persistent
---

You are a concise test agent. Follow your skills precisely. Keep responses short.`

	writeAgentMD(t, dir, "responder", responderMD)

	builderID, err := mgr.StartAgent(t.Context(), "builder", "")
	if err != nil {
		t.Fatalf("StartAgent builder: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	// Ask the builder to add a skill — the use_skill call gives it the format
	resp, err := mgr.SendMessage(ctx, builderID, `Use your create-skill skill, then add a skill called "pirate-speak" to the "responder" agent. It should instruct the agent to always respond in pirate speak using words like "arr", "matey", and "ahoy". Write it to agents/responder/skills/pirate-speak.md.`, nil)
	if err != nil {
		t.Fatalf("SendMessage to builder: %v", err)
	}
	t.Logf("Builder response: %s", resp)

	// Verify the skill file was created on disk
	skillPath := filepath.Join(dir, "agents", "responder", "skills", "pirate-speak.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("expected skill file to exist: %v", err)
	}
	t.Logf("Created skill:\n%s", string(content))

	// Start the responder and verify it uses the new skill
	responderID, err := mgr.StartAgent(ctx, "responder", "")
	if err != nil {
		t.Fatalf("StartAgent responder: %v", err)
	}

	resp, err = mgr.SendMessage(ctx, responderID, "Say hello like a pirate.", nil)
	if err != nil {
		t.Fatalf("SendMessage to responder: %v", err)
	}
	t.Logf("Responder: %s", resp)

	lower := strings.ToLower(resp)
	hasPirate := strings.Contains(lower, "arr") ||
		strings.Contains(lower, "ahoy") ||
		strings.Contains(lower, "matey")
	if !hasPirate {
		t.Errorf("expected pirate speak in response, got %q", resp)
	}
}

func TestOnline_HistoryCompaction(t *testing.T) {
	provider, apiKey, model := loadEnv(t)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Create a real LM for summarization
	lm, err := createLanguageModel(t.Context(), Options{
		Provider: provider,
		APIKey:   apiKey,
	}, model)
	if err != nil {
		t.Fatalf("createLanguageModel: %v", err)
	}

	// Open a history store
	dir := t.TempDir()
	store, err := history.OpenStore(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Use aggressive config so compaction triggers with our seeded data (~2300 tokens).
	// Budget of 2000 with 0.5 threshold = 1000 token trigger, well below our seed size.
	cfg := history.Config{
		TokenBudget:          2_000,
		FreshTailCount:       4,
		LeafChunkTokens:      500,
		LeafTargetTokens:     300,
		CondenseTargetTokens: 500,
		CompactThreshold:     0.5,
		LeafMinFanout:        4,
		CondenseMinFanout:    3,
	}
	engine := history.NewEngine(store, lm, cfg, logger)

	// Seed a realistic multi-topic conversation (~100 messages)
	conversation := []struct{ role, content string }{
		{"user", "I want to build a REST API for managing a bookstore inventory. Let's start with the database schema."},
		{"assistant", "Great idea! For a bookstore inventory API, I'd suggest starting with these core tables:\n\n1. **books** - id, title, author, isbn, price, quantity, category, created_at\n2. **authors** - id, name, bio, birth_year\n3. **categories** - id, name, description\n4. **orders** - id, customer_id, total, status, created_at\n5. **order_items** - id, order_id, book_id, quantity, price\n\nShall I write the SQL migration?"},
		{"user", "Yes, write the migration. Use PostgreSQL."},
		{"assistant", "Here's the PostgreSQL migration:\n\n```sql\nCREATE TABLE authors (\n    id SERIAL PRIMARY KEY,\n    name VARCHAR(255) NOT NULL,\n    bio TEXT,\n    birth_year INTEGER,\n    created_at TIMESTAMP DEFAULT NOW()\n);\n\nCREATE TABLE categories (\n    id SERIAL PRIMARY KEY,\n    name VARCHAR(100) NOT NULL UNIQUE,\n    description TEXT\n);\n\nCREATE TABLE books (\n    id SERIAL PRIMARY KEY,\n    title VARCHAR(500) NOT NULL,\n    author_id INTEGER REFERENCES authors(id),\n    isbn VARCHAR(13) UNIQUE,\n    price DECIMAL(10,2) NOT NULL,\n    quantity INTEGER DEFAULT 0,\n    category_id INTEGER REFERENCES categories(id),\n    created_at TIMESTAMP DEFAULT NOW()\n);\n```\n\nI've also created the orders and order_items tables. Ready for the API endpoints?"},
		{"user", "Let's set up the Go project structure first. What framework should we use?"},
		{"assistant", "For a Go REST API, I recommend:\n\n1. **chi** for routing - lightweight, idiomatic, stdlib compatible\n2. **sqlx** for database - thin wrapper over database/sql\n3. **pgx** as the PostgreSQL driver\n\nProject structure:\n```\ncmd/bookstore/main.go\ninternal/\n  api/\n    server.go\n    books.go\n    authors.go\n  db/\n    queries.go\n    migrations/\n  models/\n    book.go\n    author.go\n```"},
		{"user", "Let's use the standard library net/http instead of chi. I want minimal dependencies."},
		{"assistant", "Good call! stdlib net/http with Go 1.22+ has great routing with method patterns. Here's the server setup:\n\n```go\nmux := http.NewServeMux()\nmux.HandleFunc(\"GET /api/books\", listBooks)\nmux.HandleFunc(\"POST /api/books\", createBook)\nmux.HandleFunc(\"GET /api/books/{id}\", getBook)\nmux.HandleFunc(\"PUT /api/books/{id}\", updateBook)\nmux.HandleFunc(\"DELETE /api/books/{id}\", deleteBook)\n```\n\nNo external router needed!"},
		{"user", "Perfect. Now implement the book CRUD handlers."},
		{"assistant", "Here are the handlers:\n\n```go\nfunc (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {\n    books, err := s.db.ListBooks(r.Context())\n    if err != nil {\n        http.Error(w, err.Error(), 500)\n        return\n    }\n    json.NewEncoder(w).Encode(books)\n}\n\nfunc (s *Server) createBook(w http.ResponseWriter, r *http.Request) {\n    var book models.Book\n    if err := json.NewDecoder(r.Body).Decode(&book); err != nil {\n        http.Error(w, \"invalid request body\", 400)\n        return\n    }\n    created, err := s.db.CreateBook(r.Context(), book)\n    if err != nil {\n        http.Error(w, err.Error(), 500)\n        return\n    }\n    w.WriteHeader(201)\n    json.NewEncoder(w).Encode(created)\n}\n```\n\nSimilar pattern for getBook, updateBook, deleteBook."},
		{"user", "We need authentication. Let's add JWT-based auth."},
		{"assistant", "I'll add JWT authentication middleware:\n\n```go\nfunc (s *Server) authMiddleware(next http.Handler) http.Handler {\n    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n        token := r.Header.Get(\"Authorization\")\n        if token == \"\" {\n            http.Error(w, \"unauthorized\", 401)\n            return\n        }\n        claims, err := validateJWT(strings.TrimPrefix(token, \"Bearer \"))\n        if err != nil {\n            http.Error(w, \"invalid token\", 401)\n            return\n        }\n        ctx := context.WithValue(r.Context(), userKey, claims.UserID)\n        next.ServeHTTP(w, r.WithContext(ctx))\n    })\n}\n```"},
		{"user", "Add search functionality - users should be able to search books by title, author, and category."},
		{"assistant", "Here's the search endpoint:\n\n```go\n// GET /api/books/search?q=tolkien&category=fantasy\nfunc (s *Server) searchBooks(w http.ResponseWriter, r *http.Request) {\n    query := r.URL.Query().Get(\"q\")\n    category := r.URL.Query().Get(\"category\")\n    minPrice := r.URL.Query().Get(\"min_price\")\n    maxPrice := r.URL.Query().Get(\"max_price\")\n\n    books, err := s.db.SearchBooks(r.Context(), db.SearchParams{\n        Query:    query,\n        Category: category,\n        MinPrice: parseFloat(minPrice),\n        MaxPrice: parseFloat(maxPrice),\n    })\n    // ...\n}\n```\n\nThe SQL uses `ILIKE` for PostgreSQL full-text search."},
		{"user", "Now let's add pagination to the list and search endpoints."},
		{"assistant", "Added cursor-based pagination:\n\n```go\ntype PageParams struct {\n    Cursor string // base64-encoded last-seen ID\n    Limit  int    // default 20, max 100\n}\n\nfunc parsePagination(r *http.Request) PageParams {\n    limit, _ := strconv.Atoi(r.URL.Query().Get(\"limit\"))\n    if limit <= 0 || limit > 100 {\n        limit = 20\n    }\n    return PageParams{\n        Cursor: r.URL.Query().Get(\"cursor\"),\n        Limit:  limit,\n    }\n}\n```\n\nResponse includes a `next_cursor` field for the client to pass back."},
		{"user", "Let's write tests for the book handlers now."},
		{"assistant", "Here are the handler tests:\n\n```go\nfunc TestListBooks(t *testing.T) {\n    srv := setupTestServer(t)\n    // Seed test data\n    srv.db.CreateBook(ctx, models.Book{Title: \"The Hobbit\", Price: 12.99})\n    \n    req := httptest.NewRequest(\"GET\", \"/api/books\", nil)\n    rec := httptest.NewRecorder()\n    srv.mux.ServeHTTP(rec, req)\n    \n    require.Equal(t, 200, rec.Code)\n    var books []models.Book\n    json.NewDecoder(rec.Body).Decode(&books)\n    require.Len(t, books, 1)\n}\n\nfunc TestCreateBook_Validation(t *testing.T) {\n    srv := setupTestServer(t)\n    body := `{\"title\":\"\",\"price\":-1}`\n    req := httptest.NewRequest(\"POST\", \"/api/books\", strings.NewReader(body))\n    rec := httptest.NewRecorder()\n    srv.mux.ServeHTTP(rec, req)\n    require.Equal(t, 400, rec.Code)\n}\n```"},
		{"user", "Let's also add rate limiting to prevent abuse."},
		{"assistant", "I'll add a token bucket rate limiter middleware:\n\n```go\ntype RateLimiter struct {\n    visitors map[string]*rate.Limiter\n    mu       sync.RWMutex\n    rate     rate.Limit\n    burst    int\n}\n\nfunc (rl *RateLimiter) Middleware(next http.Handler) http.Handler {\n    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n        ip := realIP(r)\n        limiter := rl.getLimiter(ip)\n        if !limiter.Allow() {\n            http.Error(w, \"rate limit exceeded\", 429)\n            return\n        }\n        next.ServeHTTP(w, r)\n    })\n}\n```\n\nConfigured at 100 requests/minute per IP with burst of 10."},
		{"user", "We should add graceful shutdown too."},
		{"assistant", "Here's graceful shutdown:\n\n```go\nfunc main() {\n    srv := &http.Server{Addr: \":8080\", Handler: mux}\n    \n    go func() {\n        if err := srv.ListenAndServe(); err != http.ErrServerClosed {\n            log.Fatal(err)\n        }\n    }()\n    \n    quit := make(chan os.Signal, 1)\n    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)\n    <-quit\n    \n    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)\n    defer cancel()\n    srv.Shutdown(ctx)\n}\n```"},
		{"user", "One more thing - let's add structured logging with slog."},
		{"assistant", "Added slog-based structured logging:\n\n```go\nlogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{\n    Level: slog.LevelInfo,\n}))\n\n// Logging middleware\nfunc loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {\n    return func(next http.Handler) http.Handler {\n        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n            start := time.Now()\n            wrapped := &responseWriter{ResponseWriter: w}\n            next.ServeHTTP(wrapped, r)\n            logger.Info(\"request\",\n                \"method\", r.Method,\n                \"path\", r.URL.Path,\n                \"status\", wrapped.status,\n                \"duration\", time.Since(start),\n            )\n        })\n    }\n}\n```"},
		{"user", "Let's deploy this. What's the Dockerfile look like?"},
		{"assistant", "Multi-stage Dockerfile for minimal image:\n\n```dockerfile\nFROM golang:1.22-alpine AS builder\nWORKDIR /app\nCOPY go.mod go.sum ./\nRUN go mod download\nCOPY . .\nRUN CGO_ENABLED=0 go build -o bookstore ./cmd/bookstore\n\nFROM alpine:3.19\nRUN apk add --no-cache ca-certificates\nCOPY --from=builder /app/bookstore /usr/local/bin/\nEXPOSE 8080\nCMD [\"bookstore\"]\n```\n\nImage is ~15MB. Use docker-compose for local dev with PostgreSQL."},
		{"user", "Now let's add OpenAPI/Swagger documentation."},
		{"assistant", "I'll add OpenAPI 3.0 spec generation using comments:\n\n```go\n// @Summary List all books\n// @Tags books\n// @Produce json\n// @Param cursor query string false \"Pagination cursor\"\n// @Param limit query int false \"Page size (max 100)\"\n// @Success 200 {array} models.Book\n// @Router /api/books [get]\nfunc (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {\n```\n\nGenerate with `swag init -g cmd/bookstore/main.go`. Serves Swagger UI at `/docs`."},
		{"user", "Let's add a health check endpoint and readiness probe for Kubernetes."},
		{"assistant", "Health and readiness endpoints:\n\n```go\n// /healthz - always returns 200 (liveness)\nmux.HandleFunc(\"GET /healthz\", func(w http.ResponseWriter, r *http.Request) {\n    w.WriteHeader(200)\n    w.Write([]byte(\"ok\"))\n})\n\n// /readyz - checks DB connection (readiness)\nmux.HandleFunc(\"GET /readyz\", func(w http.ResponseWriter, r *http.Request) {\n    if err := db.Ping(r.Context()); err != nil {\n        http.Error(w, \"database unavailable\", 503)\n        return\n    }\n    w.WriteHeader(200)\n})\n```"},
	}

	// Seed all messages into the history store.
	// rawJSON uses placeholder content — we're testing summary-level search,
	// not message-level reconstruction, so the actual body doesn't matter.
	for _, msg := range conversation {
		rawJSON := fmt.Sprintf(`{"role":%q,"content":[{"type":"text","text":"..."}]}`, msg.role)
		if err := engine.Ingest(msg.role, msg.content, rawJSON); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	// Verify precondition: enough tokens to trigger compaction
	preTokens, err := store.ContextTokenCount()
	if err != nil {
		t.Fatalf("ContextTokenCount (pre): %v", err)
	}
	preItems, err := store.GetContextItems()
	if err != nil {
		t.Fatalf("GetContextItems (pre): %v", err)
	}
	t.Logf("Pre-compaction: %d tokens, %d context items", preTokens, len(preItems))

	threshold := int(float64(cfg.TokenBudget) * cfg.CompactThreshold)
	if preTokens < threshold {
		t.Fatalf("seeded only %d tokens but compaction threshold is %d — increase seed data or lower config",
			preTokens, threshold)
	}

	// Run compaction with the real LLM
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	if err := engine.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Check post-compaction state
	postTokens, err := store.ContextTokenCount()
	if err != nil {
		t.Fatalf("ContextTokenCount (post): %v", err)
	}
	postItems, err := store.GetContextItems()
	if err != nil {
		t.Fatalf("GetContextItems (post): %v", err)
	}
	maxDepth, err := store.MaxSummaryDepth()
	if err != nil {
		t.Fatalf("MaxSummaryDepth: %v", err)
	}

	t.Logf("Post-compaction: %d tokens, %d context items, max summary depth %d",
		postTokens, len(postItems), maxDepth)

	if maxDepth < 0 {
		t.Error("expected at least one summary to be created")
	}
	if len(postItems) >= len(preItems) {
		t.Errorf("expected fewer context items after compaction: %d -> %d", len(preItems), len(postItems))
	}
	if postTokens >= preTokens {
		t.Errorf("expected fewer tokens after compaction: %d -> %d", preTokens, postTokens)
	}

	// Verify we can search the summaries
	results, err := store.SearchSummaries("bookstore", 10)
	if err != nil {
		t.Fatalf("SearchSummaries: %v", err)
	}
	t.Logf("Search for 'bookstore': %d results", len(results))
	for _, r := range results {
		t.Logf("  [%s] %s", r.ID, r.Snippet)
	}

	// Verify assembly still works within budget
	assembled, err := engine.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	t.Logf("Assembled %d messages, ~%d tokens", len(assembled.Messages), assembled.EstimatedTokens)

	if assembled.EstimatedTokens > cfg.TokenBudget {
		t.Errorf("assembled tokens (%d) exceed budget (%d)", assembled.EstimatedTokens, cfg.TokenBudget)
	}
}
