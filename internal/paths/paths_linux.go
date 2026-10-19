//go:build linux

package paths

import (
	"os"
	"path/filepath"
)

// MediamtxName is the stream server's file name.
const MediamtxName = "mediamtx"

// AgentCmd is how the agent is spelled in messages that tell the operator what
// to run.
const AgentCmd = "kidsroom-agent"

// BinDir is where install puts the agent itself. /usr/local is the place for
// software installed outside the package manager, which is exactly what this
// is: a single binary copied in by hand.
const BinDir = "/usr/local/bin"

// InstalledBinary is the path the systemd unit starts. It is fixed rather than
// taken from os.Executable(), so that installing from /tmp or from a home
// directory still produces a unit that keeps working after that copy is gone.
func InstalledBinary() string { return filepath.Join(BinDir, AppName) }

func ConfigDir() string {
	if v := devDir(); v != "" {
		return v
	}
	return filepath.Join("/etc", AppName)
}

func StateDir() string {
	if v := devDir(); v != "" {
		return v
	}
	return filepath.Join("/var/lib", AppName)
}

func LogsDir() string {
	if v := devDir(); v != "" {
		return filepath.Join(v, "logs")
	}
	return filepath.Join("/var/log", AppName)
}

// LibDir holds mediamtx: program code, not data, so it belongs beside the
// agent under /usr/local and not in /var.
func LibDir() string {
	if v := devDir(); v != "" {
		return ExeDir()
	}
	return filepath.Join("/usr/local/lib", AppName)
}

// Dir is a directory install creates, with the mode it must end up with.
type Dir struct {
	Path string
	Mode os.FileMode
}

// InstallDirs are the directories install creates. The modes matter:
// config.json holds the camera passwords, the TLS private key and the control
// API token, so its directory is closed to everyone but root, while the page
// and the logs are ordinary files others may read.
func InstallDirs() []Dir {
	return []Dir{
		{ConfigDir(), 0o700},
		{StateDir(), 0o755},
		{LogsDir(), 0o755},
		{LibDir(), 0o755},
	}
}
