package tools

import "charm.land/fantasy"

// RemoteToolNames lists tools that execute in worker processes via gRPC.
// Used by the inference engine to determine which tools need proxy wrappers.
var RemoteToolNames = map[string]bool{
	"Bash": true, "Read": true, "Write": true,
	"Edit": true, "Glob": true, "Grep": true,
	"TaskOutput": true, "TaskStop": true,
}

// RemoteToolInfos returns the schema (ToolInfo) for all remote tools.
// The returned infos describe each tool's name, description, and parameters
// without retaining any execution capability. workingDir is used as the
// default working directory in tool descriptions.
func RemoteToolInfos(workingDir string) []fantasy.ToolInfo {
	bgMgr := NewBackgroundJobManager(nil)
	allTools := []fantasy.AgentTool{
		NewBashTool(workingDir, bgMgr),
		NewReadTool(workingDir),
		NewEditTool(workingDir),
		NewWriteTool(workingDir),
		NewGlobTool(workingDir),
		NewGrepTool(workingDir),
		NewTaskOutputTool(bgMgr),
		NewTaskStopTool(bgMgr),
	}

	infos := make([]fantasy.ToolInfo, 0, len(allTools))
	for _, t := range allTools {
		infos = append(infos, t.Info())
	}
	return infos
}
