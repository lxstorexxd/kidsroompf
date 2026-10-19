//go:build linux

package svc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kardianos/service"

	"kidsroom-agent/internal/paths"
)

// UnitFile is where the unit lands. The path is fixed by systemd, not by us.
const UnitFile = "/etc/systemd/system/" + paths.AppName + ".service"

// unitTemplate replaces the library's built-in systemd unit.
//
// The built-in one hardcodes RestartSec=120: after a crash the nursery page
// would stay dark for two minutes, and the start rate limit on top of that
// would eventually make systemd give up altogether. Five seconds and no rate
// limit are what an unattended camera agent wants — the agent already backs off
// on its own before restarting mediamtx, so systemd does not need to.
//
// The library's template engine is deliberately small: only {{Key}},
// {{Key | fn}}, {{if Key}} and {{range Key}} are understood, and an unknown key
// or function fails the install. This uses Description, Path, Arguments and
// WorkingDirectory, which are the keys it supplies, and cmd/cmdEscape, which
// are the functions it registers.
//
// KillMode is left at systemd's default, control-group: whatever the agent
// started dies with the unit, so stopping the service can never leave an
// orphaned mediamtx holding the stream ports.
const unitTemplate = `[Unit]
Description={{Description}}
Wants=network-online.target
After=network-online.target
ConditionFileIsExecutable={{Path | cmdEscape}}
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if WorkingDirectory}}WorkingDirectory={{WorkingDirectory | cmdEscape}}
{{end}}Restart=always
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`

// executablePath is the fixed installed path rather than wherever install
// happened to be run from: a unit pointing into /tmp or into a home directory
// stops working the moment that copy is cleaned up.
func executablePath() string { return paths.InstalledBinary() }

func workingDirectory() string { return paths.WorkDir() }

func serviceOptions() service.KeyValue {
	return service.KeyValue{"SystemdScript": unitTemplate}
}

// Prepare lays out everything the unit expects before the service is
// registered: the directories, the agent at its fixed path, and mediamtx.
func Prepare(logf func(string, ...any)) error {
	if os.Geteuid() != 0 {
		return errors.New("нужны права root: выполните команду через sudo")
	}

	for _, d := range paths.InstallDirs() {
		if err := os.MkdirAll(d.Path, d.Mode); err != nil {
			return err
		}
		// MkdirAll applies the umask and leaves an existing directory alone, so
		// the mode is set explicitly: the directory with the camera passwords
		// must not stay world-readable because it already existed.
		if err := os.Chmod(d.Path, d.Mode); err != nil {
			return err
		}
	}
	logf("каталоги: %s (настройки), %s (страница), %s (логи)",
		paths.ConfigDir(), paths.StateDir(), paths.LogsDir())

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := installFile(self, paths.InstalledBinary(), logf); err != nil {
		return fmt.Errorf("установка агента в %s: %w", paths.BinDir, err)
	}

	// mediamtx travels in the kit, next to the agent. Its absence is not an
	// error here: the caller downloads it instead.
	kit := filepath.Join(filepath.Dir(self), paths.MediamtxName)
	if _, err := os.Stat(kit); err == nil {
		if err := installFile(kit, paths.MediamtxBinary(), logf); err != nil {
			return fmt.Errorf("установка mediamtx: %w", err)
		}
	}
	return nil
}

// installFile copies src over dest, executable.
//
// The copy goes to a temporary name and is renamed into place, which keeps two
// cases out of trouble: src and dest being the same file, and dest being a
// binary that is currently running. Renaming over a running executable is
// allowed on Linux — the running process keeps the file it already opened —
// while writing into it fails with ETXTBSY.
func installFile(src, dest string, logf func(string, ...any)) error {
	if src == dest {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dest + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// O_CREATE applied the umask, so the mode is set explicitly.
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	logf("установлен %s", dest)
	return nil
}

func afterInstall(logf func(string, ...any)) {
	if err := EnsureFirewall(logf); err != nil {
		logf("ВНИМАНИЕ: не удалось показать нужные правила брандмауэра: %v", err)
	}
	logf("юнит:      %s", UnitFile)
	logf("настройки: %s", paths.ConfigFile())
	logf("логи:      %s", paths.LogsDir())
}

func beforeUninstall(logf func(string, ...any)) {
	logf("настройки и логи остаются на месте: %s, %s", paths.ConfigDir(), paths.LogsDir())
}
