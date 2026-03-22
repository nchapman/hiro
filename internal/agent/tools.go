package agent

import (
	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/agent/tools"
)

// buildTools creates the set of fantasy tools available to this agent.
func (a *Agent) buildTools() []fantasy.AgentTool {
	return []fantasy.AgentTool{
		tools.NewBashTool(a.workingDir),
		tools.NewReadFileTool(a.workingDir),
		tools.NewEditTool(a.workingDir),
		tools.NewWriteFileTool(a.workingDir),
		tools.NewListFilesTool(a.workingDir),
		tools.NewGlobTool(a.workingDir),
		tools.NewGrepTool(a.workingDir),
		tools.NewFetchTool(),
	}
}
