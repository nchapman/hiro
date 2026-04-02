package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent/tools"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/toolrules"
)

// RemoteTools are tools that execute in the worker process via ExecuteTool gRPC.
var RemoteTools = tools.RemoteToolNames

// proxyTool wraps a remote tool, forwarding execution to the worker via gRPC.
// Fantasy sees it as a normal AgentTool; the execution happens in the worker.
type proxyTool struct {
	info     fantasy.ToolInfo
	executor ipc.ToolExecutor
	redactor *Redactor
	logger   *slog.Logger
	opts     fantasy.ProviderOptions

	// Tool rules for call-time enforcement. Empty slices mean no restrictions.
	allowLayers [][]toolrules.Rule
	denyRules   []toolrules.Rule
}

func (t *proxyTool) Info() fantasy.ToolInfo                          { return t.info }
func (t *proxyTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *proxyTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *proxyTool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	// Enforce tool rules at call time.
	if err := t.checkRules(params); err != nil {
		t.logger.Warn("tool call denied by policy", "tool", params.Name, "error", err)
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	t.logger.Info("tool call", "tool", params.Name)
	result, err := t.executor.ExecuteTool(ctx, params.ID, params.Name, params.Input)
	if err != nil {
		t.logger.Error("tool call failed", "tool", params.Name, "error", err)
		return fantasy.ToolResponse{}, fmt.Errorf("remote tool %s: %w", params.Name, err)
	}
	content := result.Content
	if t.redactor != nil {
		content = t.redactor.Redact(content)
	}
	if result.IsError {
		t.logger.Warn("tool returned error", "tool", params.Name)
		return fantasy.NewTextErrorResponse(content), nil
	}
	return fantasy.NewTextResponse(content), nil
}

// checkRules evaluates deny and allow rules against the tool call parameters.
// Returns an error if the call is denied.
func (t *proxyTool) checkRules(tc fantasy.ToolCall) error {
	if len(t.denyRules) == 0 && len(t.allowLayers) == 0 {
		return nil
	}

	var input map[string]any
	if tc.Input != "" {
		if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
			return fmt.Errorf("tool call denied: invalid parameters")
		}
	}

	checker := &toolrules.Checker{}

	// Check deny rules (merged from all sources). Any match blocks.
	if len(t.denyRules) > 0 {
		switch checker.Check(tc.Name, input, nil, t.denyRules) {
		case toolrules.Denied:
			return fmt.Errorf("tool call denied by policy")
		case toolrules.NeedsReview:
			return fmt.Errorf("tool call denied: cannot verify against policy (command too complex)")
		}
	}

	// Check allow layers (each source must allow). Unmatched = no restriction from this source.
	for _, layer := range t.allowLayers {
		switch checker.Check(tc.Name, input, layer, nil) {
		case toolrules.Denied:
			return fmt.Errorf("tool call denied by policy")
		case toolrules.NeedsReview:
			return fmt.Errorf("tool call denied: cannot verify against policy (command too complex)")
		}
	}

	return nil
}

// buildProxyTools creates proxy tools that forward to the worker.
// Tool schemas are obtained from the tools package; execution is dispatched
// to the worker via executor. Allow/deny rules are enforced at call time.
func buildProxyTools(workingDir string, executor ipc.ToolExecutor, allowed map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule, redactor *Redactor, logger *slog.Logger) []fantasy.AgentTool {
	var proxies []fantasy.AgentTool
	for _, info := range tools.RemoteToolInfos(workingDir) {
		if allowed != nil && !allowed[info.Name] {
			continue
		}
		proxies = append(proxies, &proxyTool{
			info:        info,
			executor:    executor,
			redactor:    redactor,
			logger:      logger,
			allowLayers: allowLayers,
			denyRules:   denyRules,
		})
	}
	return proxies
}
