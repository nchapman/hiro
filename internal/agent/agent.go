// Package agent provides the Hive agent runtime. The Manager supervises
// agent session lifecycles while the inference package handles the LLM loop.
package agent

// Options configures the Manager.
type Options struct {
	WorkingDir string // working directory for file/bash tools
	Model      string // override model for all agents (from HIRO_MODEL)
}
