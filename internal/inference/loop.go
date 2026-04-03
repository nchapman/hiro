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
	"sync/atomic"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"

	"github.com/nchapman/hiro/internal/agent/tools"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/toolrules"
)

// defaultThinkingBudget is the token budget for extended thinking on older
// Anthropic models that use the binary thinking toggle.
const defaultThinkingBudget = 10_000

// LoopConfig holds all configuration needed to create a Loop.
type LoopConfig struct {
	InstanceID     string
	SessionID      string
	AgentConfig    config.AgentConfig
	Mode           config.AgentMode
	WorkingDir     string
	InstanceDir    string // instance-level state: persona.md, memory.md
	SessionDir     string // session-level state: todos.yaml, scratch/, tmp/
	AgentDefDir    string
	SharedSkillDir string
	LM             fantasy.LanguageModel
	Executor       ipc.ToolExecutor   // worker's tool executor (for remote tools)
	PDB            *platformdb.DB     // nil for ephemeral
	AllowedTools   map[string]bool    // nil = unrestricted
	AllowLayers    [][]toolrules.Rule // per-source allow rules for call-time enforcement
	DenyRules      []toolrules.Rule   // merged deny rules from all sources
	MaxTurns       int                // max agentic turns; 0 = unlimited
	HasSkills      bool
	SecretNamesFn  func() []string
	SecretEnvFn    func() []string
	Logger         *slog.Logger

	// Model is the resolved model ID (e.g. "claude-sonnet-4-20250514").
	Model string
	// Provider is the resolved provider type (e.g. "anthropic", "openrouter").
	Provider string

	// Notification queue for this instance. Owned by the instance, passed
	// to the Loop so it can expose Notify(). Must not be nil.
	Notifications *NotificationQueue

	// For building local tools — the Loop needs access to the Manager
	// for spawn/management tools. This avoids a circular dependency
	// by using the HostManager interface.
	HostManager ipc.HostManager
}

// Loop runs the fantasy agent loop for a single session.
type Loop struct {
	agent          fantasy.Agent
	instanceID     string
	sessionID      string // immutable after construction; safe to read without lock
	mode           config.AgentMode
	workingDir     string // platform root (e.g. /hiro)
	instanceDir    string // instance-level state: persona.md, memory.md
	sessionDir     string // session-level state: todos.yaml, scratch/, tmp/
	agentDefDir    string
	sharedSkillDir string
	agentConfig    config.AgentConfig
	maxTurns       int // max agentic turns; 0 = unlimited
	lm             fantasy.LanguageModel
	pdb            *platformdb.DB
	secretNamesFn  func() []string
	logger         *slog.Logger

	// Tools are stored for agent recreation on model switch.
	// Updated when skills expand the tool set.
	tools []fantasy.AgentTool

	// Session-scoped tool expansion from skills. Protected by updateMu.
	executor        ipc.ToolExecutor   // retained for creating new proxy tools
	redactor        *Redactor          // retained for creating new proxy tools
	baseDenyRules   []toolrules.Rule   // instance-level deny rules (immutable)
	baseAllowLayers [][]toolrules.Rule // instance-level allow layers (immutable)
	skillAllowLayer []toolrules.Rule   // accumulated allow rules from activated skills
	skillExpanded   bool               // true if any skill has expanded tools this session

	// Per-session model/reasoning config (protected by updateMu).
	model           string // resolved model ID (e.g. "claude-sonnet-4-20250514")
	reasoningEffort string // "" = off, "low"/"medium"/"high"/"max"/"on" = enabled
	provider        string // current provider type (e.g. "anthropic", "openrouter")

	// Notification queue for injecting meta messages (background task
	// completions, cron triggers, webhooks, etc.). Producers call Notify
	// from any goroutine; the session driver (WebSocket handler, etc.)
	// watches Notifications().Ready() and triggers turns via ChatMeta.
	notifications *NotificationQueue

	// Ephemeral message buffer (non-persistent sessions only).
	// Protected by ephemeralMu.
	ephemeralMsgs []fantasy.Message
	ephemeralMu   sync.Mutex

	// Shared skills cache for error retention.
	// Protected by skillsMu.
	lastShared []config.SkillConfig
	skillsMu   sync.Mutex

	// Compaction runs async after each turn.
	compactMu sync.Mutex

	// needsCompaction is set when the hard threshold is exceeded after async
	// compaction. The next Chat() call will run compaction synchronously
	// before assembly to prevent context overflow.
	//
	// Not persisted across restarts. On first turn after restart,
	// CompactIfNeeded falls back to estimated ContextTokenCount (lastInputTokens
	// is 0), which picks up any over-full state from the previous run.
	needsCompaction atomic.Bool

	// lastInputTokens stores the real input_tokens from the most recent API
	// call. Used by sync compaction so it can make accurate threshold decisions
	// rather than falling back to len/4 estimates. Also used to detect model
	// switches: if the stored value exceeds the new model's soft threshold,
	// sync compaction runs before assembly.
	//
	// Starts at 0 on fresh sessions and after restarts. Zero is handled as
	// "no data available" — CompactIfNeeded falls back to estimated tokens.
	lastInputTokens atomic.Int64

	// Protects mutable state: agent, agentConfig, lm, provider, reasoningEffort.
	updateMu sync.Mutex
}

// NewLoop creates an inference loop for a session.
func NewLoop(cfg LoopConfig) (*Loop, error) {
	l := &Loop{
		instanceID:     cfg.InstanceID,
		sessionID:      cfg.SessionID,
		mode:           cfg.Mode,
		workingDir:     cfg.WorkingDir,
		instanceDir:    cfg.InstanceDir,
		sessionDir:     cfg.SessionDir,
		agentDefDir:    cfg.AgentDefDir,
		sharedSkillDir: cfg.SharedSkillDir,
		agentConfig:    cfg.AgentConfig,
		maxTurns:       cfg.MaxTurns,
		lm:             cfg.LM,
		pdb:            cfg.PDB,
		secretNamesFn:  cfg.SecretNamesFn,
		notifications:  cfg.Notifications,
		logger:         cfg.Logger.With("component", "inference", "instance_id", cfg.InstanceID),
	}

	// Build tool set: remote proxy tools + local tools.
	redactor := NewRedactor(cfg.SecretEnvFn)
	agentTools := buildProxyTools(cfg.WorkingDir, cfg.Executor, cfg.AllowedTools, cfg.AllowLayers, cfg.DenyRules, redactor, l.logger)

	// Local tools: TodoWrite, HistorySearch/Recall, SpawnInstance, management tools, Skill.
	localTools := l.buildLocalTools(cfg)
	agentTools = append(agentTools, localTools...)

	// Store tools for agent recreation on model switch.
	l.tools = agentTools

	// Retain construction values for session-scoped skill tool expansion.
	l.executor = cfg.Executor
	l.redactor = redactor
	l.baseDenyRules = cfg.DenyRules
	l.baseAllowLayers = cfg.AllowLayers
	l.model = cfg.Model
	l.provider = cfg.Provider

	// Build the initial system prompt.
	systemPrompt := l.currentSystemPrompt()

	l.agent = fantasy.NewAgent(cfg.LM,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
	)

	return l, nil
}

// expandToolsForSkill adds tools granted by a skill's allowed_tools to the
// session's active tool set. Tools already available are skipped (additive
// only — skills can't restrict existing tools). Wholly denied tools are
// also skipped. Parameterized rules are accumulated into a skill allow
// layer for call-time enforcement.
//
// Must be called under no lock — acquires updateMu internally.
func (l *Loop) expandToolsForSkill(skill *config.SkillConfig) error {
	if len(skill.AllowedTools) == 0 {
		return nil
	}

	rules, err := toolrules.ParseRules(skill.AllowedTools)
	if err != nil {
		return fmt.Errorf("parsing skill %q allowed_tools: %w", skill.Name, err)
	}

	// Build set of tools the skill wants.
	wantTools := make(map[string]bool, len(rules))
	for _, r := range rules {
		wantTools[r.Tool] = true
	}

	// Build set of tools already available.
	l.updateMu.Lock()
	defer l.updateMu.Unlock()

	haveTools := make(map[string]bool, len(l.tools))
	for _, t := range l.tools {
		haveTools[t.Info().Name] = true
	}

	// Build set of wholly denied tools.
	denied := make(map[string]bool)
	for _, r := range l.baseDenyRules {
		if r.IsWholeTool() {
			denied[r.Tool] = true
		}
	}

	// Determine which tools to add.
	var newToolNames []string
	for name := range wantTools {
		if haveTools[name] {
			continue // already available
		}
		if denied[name] {
			l.logger.Warn("skill requests denied tool, skipping",
				"skill", skill.Name, "tool", name)
			continue
		}
		if !tools.RemoteToolNames[name] {
			l.logger.Warn("skill requests non-remote tool, skipping",
				"skill", skill.Name, "tool", name)
			continue
		}
		newToolNames = append(newToolNames, name)
	}

	// Only accumulate rules and create proxies if there are new tools to add.
	// Skills are additive only — they don't restrict existing tools.
	if len(newToolNames) == 0 {
		return nil
	}

	// Accumulate skill rules into a single allow layer.
	l.skillAllowLayer = append(l.skillAllowLayer, rules...)

	// Build the combined allow layers for skill-granted proxy tools:
	// base layers + skill layer.
	skillLayers := make([][]toolrules.Rule, len(l.baseAllowLayers)+1)
	copy(skillLayers, l.baseAllowLayers)
	skillLayers[len(skillLayers)-1] = l.skillAllowLayer

	// Create proxy tools for newly granted remote tools.
	{
		newNames := make(map[string]bool, len(newToolNames))
		for _, n := range newToolNames {
			newNames[n] = true
		}
		for _, info := range tools.RemoteToolInfos(l.workingDir) {
			if !newNames[info.Name] {
				continue
			}
			l.tools = append(l.tools, &proxyTool{
				info:        info,
				executor:    l.executor,
				redactor:    l.redactor,
				logger:      l.logger,
				allowLayers: skillLayers,
				denyRules:   l.baseDenyRules,
			})
		}
		l.skillExpanded = true
		l.logger.Info("skill expanded tools",
			"skill", skill.Name, "new_tools", newToolNames)
	}

	return nil
}

// Notify pushes a notification into the queue. The session driver (WebSocket
// handler, etc.) will drain it and trigger a ChatMeta turn. Safe to call from
// any goroutine. SessionID is automatically set to the loop's current session.
func (l *Loop) Notify(n Notification) {
	if n.SessionID == "" {
		n.SessionID = l.sessionID
	}
	l.notifications.Push(n)
	l.logger.Info("notification queued", "source", n.Source, "length", l.notifications.Len())
}

// Notifications returns the queue so callers can watch Ready() and Drain().
func (l *Loop) Notifications() *NotificationQueue {
	return l.notifications
}

// Chat runs one turn of the inference loop: assembles context, calls the LLM,
// dispatches tools, persists results. Files are optional image attachments for vision.
func (l *Loop) Chat(ctx context.Context, prompt string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	return l.chat(ctx, prompt, files, false, onEvent)
}

// ChatMeta runs an inference turn where the user message is stored as a meta
// message — visible to the model but hidden from the user's transcript. Used
// for notification-triggered turns (background task completions, cron, webhooks).
func (l *Loop) ChatMeta(ctx context.Context, prompt string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return l.chat(ctx, prompt, nil, true, onEvent)
}

func (l *Loop) chat(ctx context.Context, prompt string, files []fantasy.FilePart, meta bool, onEvent func(ipc.ChatEvent) error) (string, error) {
	var messages []fantasy.Message

	// Snapshot mutable state under the update lock to avoid races with UpdateModel/SetReasoningEffort.
	l.updateMu.Lock()
	agent := l.agent
	agentModel := l.model
	agentProvider := l.provider
	lm := l.lm
	providerOpts := l.buildReasoningOptionsLocked()
	l.updateMu.Unlock()

	if l.mode.IsPersistent() && l.pdb != nil {
		cfg := CompactionConfigForModel(agentModel)

		// Check whether compaction is needed before assembly. This fires in
		// two cases: (1) the hard threshold was exceeded on the previous turn
		// (needsCompaction flag), or (2) the last known context size exceeds
		// the current model's soft threshold — which catches model switches
		// (e.g., 200K → 32K) where the old context is suddenly over-full.
		lastTokens := l.lastInputTokens.Load()
		needsSync := l.needsCompaction.CompareAndSwap(true, false) ||
			(lastTokens > 0 && lastTokens >= int64(cfg.SoftThresholdTokens()))

		if needsSync {
			l.compactMu.Lock()
			compactor := NewCompactor(l.pdb, l.sessionID, &lmSummarizer{lm: lm, providerOptions: providerOpts}, cfg, l.logger)
			if result, err := compactor.CompactIfNeeded(ctx, lastTokens); err != nil {
				l.logger.Warn("synchronous compaction failed", "error", err)
			} else if result.HardThresholdExceeded {
				l.logger.Warn("context still exceeds hard threshold after synchronous compaction")
			}
			l.compactMu.Unlock()
		}

		// Assemble intentionally runs outside compactMu. If a prior turn's
		// async compaction is still in progress, SQLite WAL snapshot isolation
		// ensures Assemble sees a consistent pre- or post-compaction state.
		assembled, err := Assemble(ctx, l.pdb, l.sessionID, cfg)
		if err != nil {
			l.logger.Error("failed to assemble context, proceeding with empty history", "error", err)
		}
		if assembled.Messages != nil {
			messages = assembled.Messages
		}
	} else {
		l.ephemeralMu.Lock()
		messages = make([]fantasy.Message, len(l.ephemeralMsgs))
		copy(messages, l.ephemeralMsgs)
		l.ephemeralMu.Unlock()
	}

	emit := func(evt ipc.ChatEvent) error {
		if onEvent != nil {
			return onEvent(evt)
		}
		return nil
	}

	l.logger.Info("inference turn started", "model", agentModel, "provider", agentProvider, "history_messages", len(messages))

	call := fantasy.AgentStreamCall{
		Prompt:          prompt,
		Files:           files,
		Messages:        messages,
		ProviderOptions: providerOpts,
		PrepareStep: func(ctx context.Context, opts fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
			var result fantasy.PrepareStepResult
			if opts.StepNumber == 0 {
				sp := l.currentSystemPrompt()
				result.System = &sp
			}
			// Enforce maxTurns: disable all tools after the limit so the
			// model must produce a final text response.
			if l.maxTurns > 0 && opts.StepNumber >= l.maxTurns {
				result.DisableAllTools = true
			}
			// Inject expanded tool set if skills have added tools.
			l.updateMu.Lock()
			if l.skillExpanded {
				result.Tools = l.tools
			}
			l.updateMu.Unlock()
			return ctx, result, nil
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
		l.logger.Error("inference turn failed", "error", err)
		return "", fmt.Errorf("agent stream: %w", err)
	}

	l.logger.Info("inference turn completed", "steps", len(result.Steps))

	// Persist results.
	if l.mode.IsPersistent() && l.pdb != nil {
		l.persistTurn(ctx, prompt, files, meta, result, lm, agentModel, providerOpts)
	} else {
		l.ephemeralMu.Lock()
		l.ephemeralMsgs = append(l.ephemeralMsgs, fantasy.NewUserMessage(prompt, files...))
		for _, step := range result.Steps {
			l.ephemeralMsgs = append(l.ephemeralMsgs, step.Messages...)
		}
		l.ephemeralMu.Unlock()
	}

	// Record usage.
	if l.pdb != nil {
		l.recordUsage(ctx, result, agentModel, agentProvider)
	}

	return result.Response.Content.Text(), nil
}

// persistTurn stores the user message and all step messages in the platform DB,
// then kicks off async compaction. lm, model, and providerOpts are snapshots
// captured at the start of the turn to avoid racing with UpdateModel.
func (l *Loop) persistTurn(ctx context.Context, prompt string, files []fantasy.FilePart, meta bool, result *fantasy.AgentResult, lm fantasy.LanguageModel, model string, providerOpts fantasy.ProviderOptions) {
	rawJSON := marshalMessage(fantasy.NewUserMessage(prompt, files...))
	tokens := EstimateTokens(prompt) + EstimateFileTokens(files)
	if _, err := l.pdb.AppendMessage(ctx, l.sessionID, "user", prompt, rawJSON, tokens, meta); err != nil {
		l.logger.Warn("failed to ingest user message", "error", err)
	}

	for _, step := range result.Steps {
		for _, msg := range step.Messages {
			raw := marshalMessage(msg)
			text := extractText(msg)
			role := string(msg.Role)
			t := EstimateTokens(text)
			if _, err := l.pdb.AppendMessage(ctx, l.sessionID, role, text, raw, t); err != nil {
				l.logger.Warn("failed to ingest step message", "role", role, "error", err)
			}
		}
	}

	// Extract the last step's input_tokens — the ground truth for context size.
	var lastInputTokens int64
	if len(result.Steps) > 0 {
		lastInputTokens = result.Steps[len(result.Steps)-1].Usage.InputTokens
	}
	l.lastInputTokens.Store(lastInputTokens)

	// Async compaction — runs in background so the session mutex is released.
	// Uses the lm/model snapshots from the turn to avoid racing with UpdateModel.
	go func() {
		l.compactMu.Lock()
		defer l.compactMu.Unlock()
		compactor := NewCompactor(l.pdb, l.sessionID, &lmSummarizer{lm: lm, providerOptions: providerOpts}, CompactionConfigForModel(model), l.logger)
		compactResult, err := compactor.CompactIfNeeded(context.Background(), lastInputTokens)
		if err != nil {
			l.logger.Warn("compaction failed", "error", err)
			return
		}
		if compactResult.HardThresholdExceeded {
			l.needsCompaction.Store(true)
		}
	}()
}

// recordUsage writes one usage event per inference step to the platform DB.
// Each step corresponds to a single LLM API call, so per-step usage reflects
// the real token counts from the provider. All steps in a turn share the same
// turn number for grouping. The model and provider parameters are snapshots
// from the turn start to avoid racing with UpdateModel.
func (l *Loop) recordUsage(ctx context.Context, result *fantasy.AgentResult, model, provider string) {
	if result == nil {
		return
	}
	var events []platformdb.UsageEvent
	for _, step := range result.Steps {
		u := step.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			continue
		}
		events = append(events, platformdb.UsageEvent{
			SessionID:        l.sessionID,
			Model:            model,
			Provider:         provider,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			ReasoningTokens:  u.ReasoningTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheCreationTokens,
			Cost:             models.Cost(model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens),
		})
	}
	if len(events) > 0 {
		var totalIn, totalOut, totalReasoning int64
		var totalCost float64
		for _, e := range events {
			totalIn += e.InputTokens
			totalOut += e.OutputTokens
			totalReasoning += e.ReasoningTokens
			totalCost += e.Cost
		}
		l.logger.Info("inference usage",
			"model", model,
			"provider", provider,
			"input_tokens", totalIn,
			"output_tokens", totalOut,
			"reasoning_tokens", totalReasoning,
			"cost", totalCost,
			"steps", len(events),
		)
	}
	if err := l.pdb.RecordTurnUsage(ctx, events); err != nil {
		l.logger.Warn("failed to record usage", "error", err)
	}
}

// UpdateModel swaps the language model and recreates the fantasy agent.
// The change takes effect on the next Chat() call.
func (l *Loop) UpdateModel(lm fantasy.LanguageModel, model, provider string) {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()
	l.lm = lm
	l.model = model
	l.provider = provider
	l.agent = fantasy.NewAgent(lm,
		fantasy.WithSystemPrompt(l.currentSystemPromptWithConfig(l.agentConfig)),
		fantasy.WithTools(l.tools...),
	)
}

// UpdateToolRules rebuilds the proxy tool set with new allow/deny rules.
// Preserves local (structural) tools. Resets any session-scoped skill
// expansions since the base rules have changed. Recreates the fantasy
// agent so changes take effect on the next Chat() call.
func (l *Loop) UpdateToolRules(allowed map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule) {
	l.updateMu.Lock()
	defer l.updateMu.Unlock()

	// Rebuild proxy tools with new rules.
	proxyTools := buildProxyTools(l.workingDir, l.executor, allowed, allowLayers, denyRules, l.redactor, l.logger)

	// Preserve local (structural) tools — they bypass permission filtering.
	var newTools []fantasy.AgentTool
	newTools = append(newTools, proxyTools...)
	for _, t := range l.tools {
		if _, isProxy := t.(*proxyTool); !isProxy {
			newTools = append(newTools, t)
		}
	}

	l.tools = newTools
	l.baseDenyRules = denyRules
	l.baseAllowLayers = allowLayers
	l.skillAllowLayer = nil
	l.skillExpanded = false

	// Recreate agent with updated tools (skip if no LM, e.g. test mode).
	if l.lm != nil {
		l.agent = fantasy.NewAgent(l.lm,
			fantasy.WithSystemPrompt(l.currentSystemPromptWithConfig(l.agentConfig)),
			fantasy.WithTools(l.tools...),
		)
	}
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
	model := l.model

	switch provider {
	case "anthropic":
		if effort == "" {
			return nil
		}
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
				Thinking: &anthropic.ThinkingProviderOption{BudgetTokens: defaultThinkingBudget},
			},
		}

	case "openrouter":
		if effort == "" {
			// OpenRouter enables thinking by default for models that support it.
			// Explicitly disable it when reasoning effort is not set.
			enabled := false
			return fantasy.ProviderOptions{
				openrouter.Name: &openrouter.ProviderOptions{
					Reasoning: &openrouter.ReasoningOptions{
						Enabled: &enabled,
					},
				},
			}
		}
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

// currentSystemPrompt rebuilds the system prompt from config and disk.
// Acquires updateMu to snapshot agentConfig. Safe to call from Chat's
// PrepareStep callback. Must NOT be called while updateMu is held —
// use currentSystemPromptWithConfig instead.
func (l *Loop) currentSystemPrompt() string {
	l.updateMu.Lock()
	cfg := l.agentConfig // shallow copy
	l.updateMu.Unlock()
	return l.currentSystemPromptWithConfig(cfg)
}

// currentSystemPromptWithConfig rebuilds the system prompt using the provided
// config snapshot. Does not acquire updateMu — safe to call from UpdateModel
// which already holds the lock.
func (l *Loop) currentSystemPromptWithConfig(cfg config.AgentConfig) string {
	persona := ""
	memory := ""
	todos := ""
	// Persona and memory are instance-level state.
	if l.instanceDir != "" {
		if pd, err := config.ReadPersonaFile(l.instanceDir); err != nil {
			l.logger.Warn("could not read persona.md", "error", err)
		} else {
			persona = pd.ForPrompt()
		}
		if mem, err := config.ReadMemoryFile(l.instanceDir); err != nil {
			l.logger.Warn("could not read memory.md", "error", err)
		} else {
			memory = mem
		}
	}
	// Todos are session-level state.
	if l.sessionDir != "" {
		if t, err := config.ReadTodos(l.sessionDir); err != nil {
			l.logger.Warn("could not read todos.yaml", "error", err)
		} else {
			todos = config.FormatTodos(t)
		}
	}

	if l.agentDefDir != "" {
		prompt, err := config.ReloadAgentTexts(l.agentDefDir)
		if err != nil {
			l.logger.Warn("could not reload agent texts", "error", err)
		} else {
			cfg.Prompt = prompt
		}
	}

	if l.agentDefDir != "" {
		agentSkills, err := config.LoadSkills(filepath.Join(l.agentDefDir, "skills"))
		if err != nil {
			l.logger.Warn("could not reload agent skills", "error", err)
		} else {
			sharedSkills, sharedErr := config.LoadSkills(l.sharedSkillDir)

			l.skillsMu.Lock()
			if sharedErr != nil {
				l.logger.Warn("could not reload shared skills", "error", sharedErr)
				sharedSkills = l.lastShared
			} else {
				l.lastShared = sharedSkills
			}
			l.skillsMu.Unlock()

			cfg.Skills = config.MergeSkills(agentSkills, sharedSkills)
		}
	}

	var secretNames []string
	if l.secretNamesFn != nil {
		secretNames = l.secretNamesFn()
	}

	env := EnvInfo{
		WorkingDir:  l.workingDir,
		InstanceDir: l.instanceDir,
		SessionDir:  l.sessionDir,
		Mode:        l.mode,
	}
	return buildSystemPrompt(cfg, env, persona, memory, todos, secretNames)
}

// buildLocalTools creates tools that run in the control plane process.
func (l *Loop) buildLocalTools(cfg LoopConfig) []fantasy.AgentTool {
	var localTools []fantasy.AgentTool

	if cfg.Mode.IsPersistent() && cfg.SessionDir != "" {
		localTools = append(localTools, buildTodoTools(cfg.SessionDir)...)
	}

	if cfg.Mode.IsPersistent() && cfg.InstanceDir != "" {
		localTools = append(localTools, buildMemoryTools(cfg.InstanceDir)...)
	}

	if cfg.Mode.IsPersistent() && cfg.PDB != nil {
		localTools = append(localTools, buildHistoryTools(cfg.PDB, cfg.SessionID)...)
	}

	if cfg.HostManager != nil {
		localTools = append(localTools,
			buildSpawnTool(cfg.HostManager, l.notifications, cfg.SessionID, l.logger),
			buildCreatePersistentInstanceTool(cfg.HostManager, l.logger))
		localTools = append(localTools, buildCoordinatorTools(cfg.HostManager, l.logger)...)
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
		localTools = append(localTools, buildSkillTool(&cfg.AgentConfig, allowedDirs, l.expandToolsForSkill, l.logger))
	}

	// Filter by allowed set. All tools — both remote (worker-side) and
	// local (control-plane-side) — are subject to AllowedTools filtering.
	if cfg.AllowedTools != nil {
		filtered := make([]fantasy.AgentTool, 0, len(localTools))
		for _, t := range localTools {
			if cfg.AllowedTools[t.Info().Name] {
				filtered = append(filtered, t)
			}
		}
		localTools = filtered
	}

	return localTools
}
