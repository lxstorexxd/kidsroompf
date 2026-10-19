//go:build windows

package paths

import (
	"os"
	"path/filepath"
)

// MediamtxName is the stream server's file name.
const MediamtxName = "mediamtx.exe"

// AgentCmd is how the agent is spelled in messages that tell the operator what
// to run.
const AgentCmd = "kidsroom-agent.exe"

// DataDir is C:\ProgramData\kidsroom-agent. The program lives in Program Files
// and its data here, which is the split Windows expects: an installer may
// replace the program directory wholesale, and the settings survive it.
func DataDir() string {
	if v := devDir(); v != "" {
		return v
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		base = filepath.Join(os.Getenv("SystemDrive")+`\`, "ProgramData")
	}
	return filepath.Join(base, AppName)
}

func ConfigDir() string { return DataDir() }

func StateDir() string { return DataDir() }

func LogsDir() string { return filepath.Join(DataDir(), "logs") }

// LibDir holds mediamtx.exe: next to the agent, where the installation kit
// puts it.
func LibDir() string { return ExeDir() }
