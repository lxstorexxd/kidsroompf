package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent runs for months without supervision, so the log must stop growing
// on its own: at most keep+1 generations of maxSize each.
func TestRotationCapsDiskUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	const (
		maxSize = 1000
		keep    = 3
		line    = 100
	)
	w, err := newWriter(path, maxSize, keep)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Far more than the cap: 200 writes of 100 bytes is 20 KiB through a
	// 1000-byte file.
	payload := []byte(strings.Repeat("x", line-1) + "\n")
	for i := 0; i < 200; i++ {
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("запись %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > keep+1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("файлов %d, ожидалось не больше %d: %v", len(entries), keep+1, names)
	}

	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > maxSize {
			t.Errorf("%s вырос до %d, предел %d", e.Name(), info.Size(), maxSize)
		}
		total += info.Size()
	}
	if limit := int64(maxSize * (keep + 1)); total > limit {
		t.Errorf("суммарно %d байт, предел %d", total, limit)
	}
}

// Rotation must keep the newest lines; the oldest generation is the one that
// gets dropped.
func TestRotationKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := newWriter(path, 50, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for _, s := range []string{"первая\n", "вторая\n", "третья\n", "четвёртая\n"} {
		// Each line is padded so that every write forces a rotation.
		if _, err := w.Write([]byte(s + strings.Repeat(" ", 45) + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "четвёртая") {
		t.Errorf("в текущем файле нет последней строки: %q", string(current))
	}

	previous, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("нет предыдущего поколения: %v", err)
	}
	if !strings.Contains(string(previous), "третья") {
		t.Errorf("в .1 ожидалась предпоследняя строка, получено %q", string(previous))
	}
}

// Reopening an existing log must continue it, not start counting from zero,
// or a restart loop would let the file grow past the limit.
func TestReopenPicksUpExistingSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	if err := os.WriteFile(path, []byte(strings.Repeat("x", 900)), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := newWriter(path, 1000, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("y", 200))); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error("запись поверх почти полного файла должна была вызвать ротацию")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 200 {
		t.Errorf("новый файл содержит %d байт, ожидалось 200", info.Size())
	}
}

// A single line longer than the limit must still be written instead of
// triggering a rotation on every call.
func TestOversizedWriteGoesThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := newWriter(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	big := []byte(strings.Repeat("z", 500))
	for i := 0; i < 3; i++ {
		if n, err := w.Write(big); err != nil || n != len(big) {
			t.Fatalf("запись %d: n=%d err=%v", i, n, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 3 {
		t.Errorf("файлов %d, ожидалось не больше 3", len(entries))
	}
}
