//go:build linux

package paths

import (
	"path/filepath"
	"testing"
)

// TestLinuxPathsFollowFHS pins the layout an administrator is told to expect.
// These paths appear in the documentation, in the systemd unit and in the
// install output, so a change here is a change to all three.
func TestLinuxPathsFollowFHS(t *testing.T) {
	t.Setenv("KIDSROOM_DATA_DIR", "")

	cases := []struct {
		what string
		got  string
		want string
	}{
		{"настройки", ConfigFile(), "/etc/kidsroom-agent/config.json"},
		{"mediamtx.yml", MediamtxConfigFile(), "/etc/kidsroom-agent/mediamtx.yml"},
		{"страница", DefaultWebDir(), "/var/lib/kidsroom-agent/web"},
		{"адрес control API", EndpointFile(), "/var/lib/kidsroom-agent/control-endpoint.txt"},
		{"лог агента", AgentLogFile(), "/var/log/kidsroom-agent/agent.log"},
		{"лог mediamtx", MediamtxLogFile(), "/var/log/kidsroom-agent/mediamtx.log"},
		{"mediamtx", MediamtxBinary(), "/usr/local/lib/kidsroom-agent/mediamtx"},
		{"агент", InstalledBinary(), "/usr/local/bin/kidsroom-agent"},
		{"рабочий каталог", WorkDir(), "/var/lib/kidsroom-agent"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %s, ожидалось %s", c.what, c.got, c.want)
		}
	}
}

// TestDataDirOverrideKeepsEverythingTogether covers the stand and development
// runs: with KIDSROOM_DATA_DIR set, nothing may be read from or written to the
// system directories of an installed agent, and mediamtx is taken from the
// build directory rather than from /usr/local.
func TestDataDirOverrideKeepsEverythingTogether(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIDSROOM_DATA_DIR", dir)

	if got, want := ConfigFile(), filepath.Join(dir, "config.json"); got != want {
		t.Errorf("настройки: %s, ожидалось %s", got, want)
	}
	if got, want := DefaultWebDir(), filepath.Join(dir, "web"); got != want {
		t.Errorf("страница: %s, ожидалось %s", got, want)
	}
	if got, want := LogsDir(), filepath.Join(dir, "logs"); got != want {
		t.Errorf("логи: %s, ожидалось %s", got, want)
	}
	if got, want := MediamtxBinary(), filepath.Join(ExeDir(), "mediamtx"); got != want {
		t.Errorf("mediamtx: %s, ожидалось %s", got, want)
	}
	for _, d := range InstallDirs() {
		if filepath.Dir(d.Path) == "/etc" || filepath.Dir(d.Path) == "/var/lib" {
			t.Errorf("установка задела системный каталог %s при заданном KIDSROOM_DATA_DIR", d.Path)
		}
	}
}

// TestConfigDirIsClosed guards the one mode that matters: config.json holds the
// camera passwords, the TLS private key and the control API token.
func TestConfigDirIsClosed(t *testing.T) {
	t.Setenv("KIDSROOM_DATA_DIR", "")
	for _, d := range InstallDirs() {
		if d.Path != ConfigDir() {
			continue
		}
		if d.Mode != 0o700 {
			t.Fatalf("каталог настроек с правами %o, ожидалось 700", d.Mode)
		}
		return
	}
	t.Fatal("каталога настроек нет в списке создаваемых")
}
