//go:build linux

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveClosesTheConfigFile is the Linux counterpart of the icacls hardening
// on Windows. config.json holds every camera password, the TLS private key and
// the control API token; on a machine where others have shell access, a
// world-readable copy of it hands over the whole site.
func TestSaveClosesTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config.json с правами %o: секреты площадки доступны не только root", mode)
	}
}

// TestSaveClosesAnAlreadyOpenFile covers the second write. Save renames a
// temporary file over the old one, so the mode has to be applied every time
// rather than only when the file is created — otherwise the first save of a
// file that someone had chmod 644 would leave it that way.
func TestSaveClosesAnAlreadyOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("после повторной записи права %o, ожидалось 600", mode)
	}
}
