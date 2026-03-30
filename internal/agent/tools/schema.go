package tools

import "charm.land/fantasy"

// RemoteToolNames lists tools that execute in worker processes via gRPC.
// Used by the inference engine to determine which tools need proxy wrappers.
var RemoteToolNames = map[string]bool{
	"bash": true, "read_file": true, "write_file": true,
	"edit_file": true, "multiedit_file": true, "list_files": true,
	"glob": true, "grep": true, "fetch": true,
	"job_output": true, "job_kill": true,
}

// RemoteToolInfos returns the schema (ToolInfo) for all remote tools.
// The returned infos describe each tool's name, description, and parameters
// without retaining any execution capability. workingDir is used as the
// default working directory in tool descriptions.
func RemoteToolInfos(workingDir string) []fantasy.ToolInfo {
	bgMgr := NewBackgroundJobManager(nil)
	allTools := []fantasy.AgentTool{
		NewBashTool(workingDir, bgMgr),
		NewReadFileTool(workingDir),
		NewEditTool(workingDir),
		NewMultiEditTool(workingDir),
		NewWriteFileTool(workingDir),
		NewListFilesTool(workingDir),
		NewGlobTool(workingDir),
		NewGrepTool(workingDir),
		NewFetchTool(),
		NewJobOutputTool(bgMgr),
		NewJobKillTool(bgMgr),
	}

	infos := make([]fantasy.ToolInfo, 0, len(allTools))
	for _, t := range allTools {
		infos = append(infos, t.Info())
	}
	return infos
}
