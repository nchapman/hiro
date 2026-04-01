package inference

import (
	"context"
	"fmt"
	"log/slog"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent/tools"
	"github.com/nchapman/hiro/internal/ipc"
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
}

func (t *proxyTool) Info() fantasy.ToolInfo                        { return t.info }
func (t *proxyTool) ProviderOptions() fantasy.ProviderOptions      { return t.opts }
func (t *proxyTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *proxyTool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

// buildProxyTools creates proxy tools that forward to the worker.
// Tool schemas are obtained from the tools package; execution is dispatched
// to the worker via executor.
func buildProxyTools(workingDir string, executor ipc.ToolExecutor, allowed map[string]bool, redactor *Redactor, logger *slog.Logger) []fantasy.AgentTool {
	var proxies []fantasy.AgentTool
	for _, info := range tools.RemoteToolInfos(workingDir) {
		if allowed != nil && !allowed[info.Name] {
			continue
		}
		proxies = append(proxies, &proxyTool{
			info:     info,
			executor: executor,
			redactor: redactor,
			logger:   logger,
		})
	}
	return proxies
}
