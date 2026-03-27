// Package inference runs the LLM inference loop in the control plane.
// Each running session gets a Loop that drives the fantasy agent,
// assembles system prompts, and dispatches tool calls to workers.
package inference

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/models"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
)

// LoopConfig holds all configuration needed to create a Loop.
type LoopConfig struct {
	SessionID      string
	AgentConfig    config.AgentConfig
	Mode           config.AgentMode
	WorkingDir     string
	SessionDir     string
	AgentDefDir    string
	SharedSkillDir string
	LM             fantasy.LanguageModel
	Executor       ipc.ToolExecutor       // worker's tool executor (for remote tools)
	PDB            *platformdb.DB         // nil for ephemeral
	AllowedTools   map[string]bool        // nil = unrestricted
	HasSkills      bool
	SecretNamesFn  func() []string
	SecretEnvFn    func() []string
	Logger         *slog.Logger

	// For building local tools — the Loop needs access to the Manager
	// for coordinator/spawn tools. This avoids a circular dependency
	// by using the HostManager interface.
	HostManager ipc.HostManager
	CallerMode  config.AgentMode
}

// Loop runs the fantasy agent loop for a single session.
type Loop struct {
	agent          fantasy.Agent
	sessionID      string
	mode           config.AgentMode
	sessionDir     string
	agentDefDir    string
	sharedSkillDir string
	agentConfig    config.AgentConfig
	lm             fantasy.LanguageModel
	pdb            *platformdb.DB
	secretNamesFn  func() []string
	logger         *slog.Logger

	// Tools are stored for agent recreation on model switch.
	tools []fantasy.AgentTool

	// Per-session reasoning config (protected by updateMu).
	reasoningEffort string // "" = off, "low"/"medium"/"high"/"max"/"on" = enabled
	provider        string // current provider type (e.g. "anthropic", "openrouter")

	// Ephemeral message buffer (non-persistent sessions only).
	ephemeralMsgs []fantasy.Message

	// Shared skills cache for error retention.
	lastShared []config.SkillConfig

	// Compaction runs async after each turn.
	compactMu sync.Mutex

	// Config update support — applied at the start of the next Chat call.
	updateMu      sync.Mutex
	pendingUpdate *ipc.ConfigUpdate
}

// NewLoop creates an inference loop for a session.
func NewLoop(cfg LoopConfig) (*Loop, error) {
	l := &Loop{
		sessionID:      cfg.SessionID,
		mode:           cfg.Mode,
		sessionDir:     cfg.SessionDir,
		agentDefDir:    cfg.AgentDefDir,
		sharedSkillDir: cfg.SharedSkillDir,
		agentConfig:    cfg.AgentConfig,
		lm:             cfg.LM,
		pdb:            cfg.PDB,
		secretNamesFn:  cfg.SecretNamesFn,
		logger:         cfg.Logger,
	}

	// Build tool set: remote proxy tools + local tools.
	redactor := NewRedactor(cfg.SecretEnvFn)
	agentTools := buildProxyTools(cfg.WorkingDir, cfg.Executor, cfg.AllowedTools, redactor)

	// Local tools: memory, todos, history, spawn, coordinator, use_skill.
	localTools := l.buildLocalTools(cfg)
	agentTools = append(agentTools, localTools...)

	// Store tools for agent recreation on model switch.
	l.tools = agentTools
	l.provider = cfg.AgentConfig.Provider

	// Build the initial system prompt.
	systemPrompt := l.currentSystemPrompt()

	l.agent = fantasy.NewAgent(cfg.LM,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
	)

	return l, nil
}

// Chat runs one turn of the inference loop: assembles context, calls the LLM,
// dispatches tools, persists results. Files are optional image attachments for vision.
func (l *Loop) Chat(ctx context.Context, prompt string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	var messages []fantasy.Message

	if l.mode.IsPersistent() && l.pdb != nil {
		assembled, err := Assemble(l.pdb, l.sessionID, CompactionConfigForModel(l.agentConfig.Model))
		if err != nil {
			l.logger.Warn("failed to assemble context, falling back to empty", "error", err)
		}
		messages = assembled.Messages
	} else {
		messages = l.ephemeralMsgs
	}

	emit := func(evt ipc.ChatEvent) error {
		if onEvent != nil {
			return onEvent(evt)
		}
		return nil
	}

	// Snapshot mutable state under the update lock to avoid races with UpdateModel/SetReasoningEffort.
	l.updateMu.Lock()
	agent := l.agent
	providerOpts := l.buildReasoningOptionsLocked()
	l.updateMu.Unlock()

	call := fantasy.AgentStreamCall{
		Prompt:          prompt,
		Files:           files,
		Messages:        messages,
		ProviderOptions: providerOpts,
		PrepareStep: func(ctx context.Context, opts fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
			if opts.StepNumber == 0 {
				sp := l.currentSystemPrompt()
				return ctx, fantasy.PrepareStepResult{System: &sp}, nil
			}
			return ctx, fantasy.PrepareStepResult{}, nil
		},
		OnReasoningStart: func(id string, rc fantasy.ReasoningContent) error {
			return emit(ipc.ChatEvent{Type: "reasoning_start"})
		},
		OnReasoningDelta: func(id, text string) error {
			return emit(ipc.ChatEvent{Type: "reasoning_delta", Content: text})
		},
		OnReasoningEnd: func(id string, rc fantasy.ReasoningContent) error {
			return emit(ipc.ChatEvent{Type: "reasoning_end"})
		},
		OnTextDelta: func(id, text string) error {
			return emit(ipc.ChatEvent{Type: "delta", Content: text})
		},
		OnToolCall: func(tc fantasy.ToolCallContent) error {
			return emit(ipc.ChatEvent{
				Type:       "tool_call",
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Input:      tc.Input,
				Status:     resolveStatusMessage(tc.ToolName, tc.Input),
			})
		},
		OnToolResult: func(tr fantasy.ToolResultContent) error {
			output, isErr := extractToolResultOutput(tr.Result)
			return emit(ipc.ChatEvent{
				Type:       "tool_result",
				ToolCallID: tr.ToolCallID,
				Output:     output,
				IsError:    isErr,
			})
		},
	}

	result, err := agent.Stream(ctx, call)
	if err != nil {
		return "", fmt.Errorf("agent stream: %w", err)
	}

	// Persist results.
	if l.mode.IsPersistent() && l.pdb != nil {
		l.persistTurn(ctx, prompt, files, result)
	} else {
		l.ephemeralMsgs = append(l.ephemeralMsgs, fantasy.NewUserMessage(prompt, files...))
		for _, step := range result.Steps {
			l.ephemeralMsgs = append(l.ephemeralMsgs, step.Messages...)
		}
	}

	// Record usage.
	if l.pdb != nil {
		l.recordUsage(result)
	}

	return result.Response.Content.Text(), nil
}

// persistTurn stores the user message and all step messages in the platform DB,
// then kicks off async compaction.
func (l *Loop) persistTurn(ctx context.Context, prompt string, files []fantasy.FilePart, result *fantasy.AgentResult) {
	rawJSON := marshalMessage(fantasy.NewUserMessage(prompt, files...))
	tokens := EstimateTokens(prompt) + EstimateFileTokens(len(files))
	if _, err := l.pdb.AppendMessage(l.sessionID, "user", prompt, rawJSON, tokens); err != nil {
		l.logger.Warn("failed to ingest user message", "error", err)
	}

	for _, step := range result.Steps {
		for _, msg := range step.Messages {
			raw := marshalMessage(msg)
			text := extractText(msg)
			role := string(msg.Role)
			t := EstimateTokens(text)
			if _, err := l.pdb.AppendMessage(l.sessionID, role, text, raw, t); err != nil {
				l.logger.Warn("failed to ingest step message", "role", role, "error", err)
			}
		}
	}

	// Async compaction — runs in background so the session mutex is released.
	go func() {
		l.compactMu.Lock()
		defer l.compactMu.Unlock()
		compactor := NewCompactor(l.pdb, l.sessionID, &lmSummarizer{lm: l.lm}, CompactionConfigForModel(l.agentConfig.Model), l.logger)
		if err := compactor.CompactIfNeeded(context.Background()); err != nil {
			l.logger.Warn("compaction failed", "error", err)
		}
	}()
}

// recordUsage writes a usage event to the platform DB.
func (l *Loop) recordUsage(result *fantasy.AgentResult) {
	if result.TotalUsage.InputTokens == 0 && result.TotalUsage.OutputTokens == 0 {
		return
	}
	u := result.TotalUsage
	l.pdb.RecordUsage(platformdb.UsageEvent{
		SessionID:        l.sessionID,
		Model:            l.agentConfig.Model,
		Provider:         l.agentConfig.Provider,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		ReasoningTokens:  u.ReasoningTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheCreationTokens,
		Cost:             models.Cost(l.agentConfig.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens),
	})
}

// UpdateModel swaps the language model and recreates the fantasy agent.
// The change takes effect on the next Chat() call.
func (l *Loop) UpdateModel(lm fantasy.LanguageModel, model, provider string) {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	l.lm = lm
	l.agentConfig.Model = model
	l.agentConfig.Provider = provider
	l.provider = provider
	l.agent = fantasy.NewAgent(lm,
		fantasy.WithSystemPrompt(l.currentSystemPrompt()),
		fantasy.WithTools(l.tools...),
	)
}

// SetReasoningEffort sets the reasoning effort level for subsequent calls.
// An empty string disables reasoning.
func (l *Loop) SetReasoningEffort(effort string) {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	l.reasoningEffort = effort
}

// ReasoningEffort returns the current reasoning effort level.
func (l *Loop) ReasoningEffort() string {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	return l.reasoningEffort
}

// buildReasoningOptionsLocked creates provider-specific ProviderOptions for reasoning.
// Must be called with updateMu held. Returns nil if reasoning is disabled.
func (l *Loop) buildReasoningOptionsLocked() fantasy.ProviderOptions {
	effort := l.reasoningEffort
	provider := l.provider
	model := l.agentConfig.Model

	if effort == "" {
		return nil
	}

	switch provider {
	case "anthropic":
		m, _ := models.Lookup(model)
		if len(m.ReasoningLevels) > 0 {
			// New models with effort levels.
			e := anthropic.Effort(effort)
			return fantasy.ProviderOptions{
				anthropic.Name: &anthropic.ProviderOptions{Effort: &e},
			}
		}
		// Older models with binary thinking toggle.
		return fantasy.ProviderOptions{
			anthropic.Name: &anthropic.ProviderOptions{
				Thinking: &anthropic.ThinkingProviderOption{BudgetTokens: 10_000},
			},
		}

	case "openrouter":
		e := openrouter.ReasoningEffort(effort)
		enabled := true
		return fantasy.ProviderOptions{
			openrouter.Name: &openrouter.ProviderOptions{
				Reasoning: &openrouter.ReasoningOptions{
					Enabled: &enabled,
					Effort:  &e,
				},
			},
		}

	default:
		return nil
	}
}

// ApplyConfigUpdate stores a pending config update. It will take effect
// at the start of the next Chat call's PrepareStep.
func (l *Loop) ApplyConfigUpdate(update ipc.ConfigUpdate) {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	l.pendingUpdate = &update
}

// consumePendingUpdate atomically retrieves and clears the pending config update.
func (l *Loop) consumePendingUpdate() *ipc.ConfigUpdate {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	u := l.pendingUpdate
	l.pendingUpdate = nil
	return u
}

// currentSystemPrompt rebuilds the system prompt from config and disk.
func (l *Loop) currentSystemPrompt() string {
	identity := ""
	memory := ""
	todos := ""
	if l.sessionDir != "" {
		if id, err := config.ReadOptionalFile(filepath.Join(l.sessionDir, "identity.md")); err != nil {
			l.logger.Warn("could not read identity.md", "error", err)
		} else {
			identity = id
		}
		if mem, err := config.ReadMemoryFile(l.sessionDir); err != nil {
			l.logger.Warn("could not read memory.md", "error", err)
		} else {
			memory = mem
		}
		if t, err := config.ReadTodos(l.sessionDir); err != nil {
			l.logger.Warn("could not read todos.yaml", "error", err)
		} else {
			todos = config.FormatTodos(t)
		}
	}

	if l.agentDefDir != "" {
		prompt, soul, toolNotes, err := config.ReloadAgentTexts(l.agentDefDir)
		if err != nil {
			l.logger.Warn("could not reload agent texts", "error", err)
		} else {
			l.agentConfig.Prompt = prompt
			l.agentConfig.Soul = soul
			l.agentConfig.Tools = toolNotes
		}
	}

	if l.agentDefDir != "" {
		agentSkills, err := config.LoadSkills(filepath.Join(l.agentDefDir, "skills"))
		if err != nil {
			l.logger.Warn("could not reload agent skills", "error", err)
		} else {
			sharedSkills, sharedErr := config.LoadSkills(l.sharedSkillDir)
			if sharedErr != nil {
				l.logger.Warn("could not reload shared skills", "error", sharedErr)
				sharedSkills = l.lastShared
			} else {
				l.lastShared = sharedSkills
			}
			l.agentConfig.Skills = config.MergeSkills(agentSkills, sharedSkills)
		}
	}

	var secretNames []string
	if l.secretNamesFn != nil {
		secretNames = l.secretNamesFn()
	}

	return buildSystemPrompt(l.agentConfig, identity, memory, todos, secretNames)
}

// buildLocalTools creates tools that run in the control plane process.
func (l *Loop) buildLocalTools(cfg LoopConfig) []fantasy.AgentTool {
	var localTools []fantasy.AgentTool

	if cfg.Mode.IsPersistent() && cfg.SessionDir != "" {
		localTools = append(localTools, buildMemoryTools(cfg.SessionDir)...)
		localTools = append(localTools, buildTodoTools(cfg.SessionDir)...)
	}

	if cfg.Mode.IsPersistent() && cfg.PDB != nil {
		localTools = append(localTools, buildHistoryTools(cfg.PDB, cfg.SessionID)...)
	}

	if cfg.HostManager != nil {
		localTools = append(localTools, buildSpawnTool(cfg.HostManager, cfg.CallerMode))
		if cfg.CallerMode == config.ModeCoordinator {
			localTools = append(localTools, buildCoordinatorTools(cfg.HostManager)...)
		}
	}

	// Skill tool.
	if cfg.HasSkills {
		var allowedDirs []string
		if cfg.AgentDefDir != "" {
			dir := filepath.Join(cfg.AgentDefDir, "skills")
			allowedDirs = append(allowedDirs, dir)
		}
		if cfg.SharedSkillDir != "" {
			allowedDirs = append(allowedDirs, cfg.SharedSkillDir)
		}
		localTools = append(localTools, buildSkillTool(&cfg.AgentConfig, allowedDirs))
	}

	// Filter by allowed set.
	if cfg.AllowedTools != nil {
		filtered := make([]fantasy.AgentTool, 0, len(localTools))
		for _, t := range localTools {
			name := t.Info().Name
			// Local tools that aren't in the remote set are structural
			// (spawn, coordinator, memory, etc.) and bypass tool filtering.
			if RemoteTools[name] && !cfg.AllowedTools[name] {
				continue
			}
			filtered = append(filtered, t)
		}
		localTools = filtered
	}

	return localTools
}
