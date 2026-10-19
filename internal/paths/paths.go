// Package paths resolves the on-disk locations used by the agent.
//
// Windows keeps everything the agent owns in one directory under ProgramData.
// Linux spreads the same files over /etc, /var/lib and /var/log, because that
// is where an administrator of a Debian machine looks for them. Nothing above
// this package builds a path of its own, so the whole difference lives here.
package paths

import (
	"os"
	"path/filepath"
)

// AppName is the service name and the directory name under ProgramData, /etc
// and /var.
const AppName = "kidsroom-agent"

// devDir is KIDSROOM_DATA_DIR: one directory holding the entire state of one
// agent. It exists for development and for the two-agent test stand, where an
// installed instance must not be touched — and on Linux it also makes a run
// without root possible.
func devDir() string { return os.Getenv("KIDSROOM_DATA_DIR") }

// ConfigFile is the only file an operator edits by hand.
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.json") }

// MediamtxConfigFile is generated from config.json at every start. It sits
// beside the configuration rather than among the state files so that
// everything worth reading while diagnosing a problem is in one directory.
func MediamtxConfigFile() string { return filepath.Join(ConfigDir(), "mediamtx.yml") }

func DefaultWebDir() string { return filepath.Join(StateDir(), "web") }

// EndpointFile holds the loopback URL of the control API. It carries no secret
// and stays readable for everyone, so a local command can find the API without
// read access to config.json.
func EndpointFile() string { return filepath.Join(StateDir(), "control-endpoint.txt") }

func AgentLogFile() string { return filepath.Join(LogsDir(), "agent.log") }

func MediamtxLogFile() string { return filepath.Join(LogsDir(), "mediamtx.log") }

// ExeDir is the directory of the running binary. On Windows it holds mediamtx
// as well; on Linux it is where the installation kit was unpacked.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// MediamtxBinary is the stream server the agent supervises.
func MediamtxBinary() string { return filepath.Join(LibDir(), MediamtxName) }

// WorkDir is the working directory for the agent and for mediamtx. mediamtx
// resolves relative paths from its own configuration against it, so it must
// exist and must be writable.
func WorkDir() string { return StateDir() }
