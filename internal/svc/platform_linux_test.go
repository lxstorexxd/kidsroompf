//go:build linux

package svc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The keys and functions github.com/kardianos/service v1.3.0 supplies when it
// renders a systemd unit (service_systemd_linux.go, Install; service_linux.go,
// the function table). Its template engine is not text/template: it fails the
// install outright on a key or a function it does not know, and it does so
// after creating an empty unit file.
var (
	unitKeys = []string{
		"Description", "Path", "Name", "Dependencies", "Arguments", "ChRoot",
		"WorkingDirectory", "UserName", "ReloadSignal", "PIDFile", "LogDirectory",
		"OutputFileSupport", "LimitNOFILE", "Restart", "SuccessExitStatus", "EnvVars",
	}
	unitFuncs = []string{"cmd", "cmdEscape"}
)

// TestUnitTemplateUsesOnlyWhatTheLibraryProvides is the one thing about this
// template that compiling cannot check. It matters because the failure happens
// on the target machine during install, which is the worst place to discover
// a typo: fifty sites away, with a half-written unit file left behind.
func TestUnitTemplateUsesOnlyWhatTheLibraryProvides(t *testing.T) {
	for _, action := range templateActions(t, unitTemplate) {
		parts := strings.Split(action, "|")

		key := strings.TrimSpace(parts[0])
		switch {
		case key == "end" || key == "else" || key == ".":
			// Keywords and the current range element need no lookup.
		default:
			key = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(key, "if"), "range"))
			if !contains(unitKeys, key) {
				t.Errorf("шаблон юнита обращается к ключу %q, которого библиотека не передаёт", key)
			}
		}

		for _, fn := range parts[1:] {
			if fn := strings.TrimSpace(fn); !contains(unitFuncs, fn) {
				t.Errorf("шаблон юнита вызывает функцию %q, которой у библиотеки нет", fn)
			}
		}
	}
}

// TestUnitTemplateRestartsQuickly pins the reason this template exists at all:
// the built-in one waits two minutes before restarting a crashed service, and
// gives up entirely after a few attempts.
func TestUnitTemplateRestartsQuickly(t *testing.T) {
	want := []string{
		"Restart=always",
		"RestartSec=5",
		"StartLimitIntervalSec=0",
		"WantedBy=multi-user.target",
		"After=network-online.target",
	}
	for _, line := range want {
		if !strings.Contains(unitTemplate, line) {
			t.Errorf("в юните нет %q", line)
		}
	}
	if strings.Contains(unitTemplate, "RestartSec=120") {
		t.Error("в юните осталась задержка перезапуска из шаблона библиотеки")
	}
}

// templateActions returns the contents of every {{...}} in the template.
func templateActions(t *testing.T, src string) []string {
	t.Helper()

	var out []string
	for {
		open := strings.Index(src, "{{")
		if open < 0 {
			return out
		}
		rest := src[open+2:]
		close := strings.Index(rest, "}}")
		if close < 0 {
			t.Fatal("в шаблоне юнита незакрытая {{")
		}
		out = append(out, rest[:close])
		src = rest[close+2:]
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestInstallFileReplacesARunningBinary: install copies the agent over the
// previous one, which on an upgrade is the file systemd is running. Writing
// into it would fail with ETXTBSY, so the copy has to be renamed into place.
func TestInstallFileReplacesARunningBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new", "kidsroom-agent")
	dest := filepath.Join(dir, "bin", "kidsroom-agent")
	write(t, src, "новая версия", 0o755)
	write(t, dest, "старая версия", 0o755)

	if err := installFile(src, dest, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}

	if got := read(t, dest); got != "новая версия" {
		t.Fatalf("на месте осталось %q", got)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("права %o, ожидалось 755", st.Mode().Perm())
	}
	if _, err := os.Stat(dest + ".new"); err == nil {
		t.Fatal("рядом остался временный файл")
	}
}

// TestInstallFileLeavesTheSameFileAlone covers running install from the
// installed copy — "sudo kidsroom-agent install" after an uninstall. Copying a
// file onto itself truncates it, which would delete the agent.
func TestInstallFileLeavesTheSameFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kidsroom-agent")
	write(t, path, "рабочий агент", 0o755)

	if err := installFile(path, path, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "рабочий агент" {
		t.Fatalf("агент повреждён: %q", got)
	}
}

// TestInstallFileSetsTheModeDespiteUmask: O_CREATE applies the umask, so a
// machine with umask 077 would otherwise get a binary nobody but root can run,
// and one with umask 0 a world-writable one.
func TestInstallFileSetsTheModeDespiteUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	write(t, src, "бинарник", 0o755)

	if err := installFile(src, dest, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("права %o, ожидалось 755", st.Mode().Perm())
	}
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
