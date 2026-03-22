package history

import (
	"context"
	"fmt"
	"log/slog"

	"charm.land/fantasy"
)

// Engine manages conversation history for a single agent session.
// It coordinates message storage, context assembly, and automatic compaction.
type Engine struct {
	store     *Store
	compactor *Compactor
	config    Config
	logger    *slog.Logger
}

// lmSummarizer adapts a fantasy.LanguageModel to the Summarizer interface.
type lmSummarizer struct {
	lm fantasy.LanguageModel
}

func (s *lmSummarizer) Summarize(ctx context.Context, systemPrompt, input string) (string, error) {
	resp, err := s.lm.Generate(ctx, fantasy.Call{
		Prompt: fantasy.Prompt{
			fantasy.NewSystemMessage(systemPrompt),
			fantasy.NewUserMessage(input),
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Content.Text(), nil
}

// NewEngine creates a new history engine. The language model is used
// for summarization during compaction.
func NewEngine(store *Store, lm fantasy.LanguageModel, cfg Config, logger *slog.Logger) *Engine {
	summarizer := &lmSummarizer{lm: lm}
	return &Engine{
		store:     store,
		compactor: NewCompactor(store, summarizer, cfg, logger),
		config:    cfg,
		logger:    logger,
	}
}

// NewEngineWithSummarizer creates a history engine with a custom summarizer.
// Useful for testing without an LLM.
func NewEngineWithSummarizer(store *Store, summarizer Summarizer, cfg Config, logger *slog.Logger) *Engine {
	return &Engine{
		store:     store,
		compactor: NewCompactor(store, summarizer, cfg, logger),
		config:    cfg,
		logger:    logger,
	}
}

// Store returns the underlying store for direct queries (e.g., search tools).
func (e *Engine) Store() *Store {
	return e.store
}

// Ingest stores a message in the history. Call this for each user/assistant
// message in the conversation.
func (e *Engine) Ingest(role, content, rawJSON string) error {
	tokens := EstimateTokens(content)
	_, err := e.store.AppendMessage(role, content, rawJSON, tokens)
	if err != nil {
		return fmt.Errorf("appending message: %w", err)
	}
	return nil
}

// Compact runs incremental compaction if thresholds are met.
// Call after each conversation turn.
func (e *Engine) Compact(ctx context.Context) error {
	return e.compactor.CompactIfNeeded(ctx)
}

// Assemble builds the message list for the next LLM call.
func (e *Engine) Assemble() (AssembleResult, error) {
	return Assemble(e.store, e.config)
}

// Close closes the underlying store.
func (e *Engine) Close() error {
	return e.store.Close()
}
