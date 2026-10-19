//go:build linux

package mediamtx

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarball writes a gzipped tar with the given entries, as the published
// release does: the binary plus files the agent must not take.
func makeTarball(t *testing.T, entries map[string]string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mediamtx.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExtractBinaryTakesOnlyMediamtx: the release tarball also carries a
// mediamtx.yml, and that one must never land on disk — the agent generates the
// configuration itself from config.json, and a stray copy beside it is exactly
// the kind of file someone edits by mistake while diagnosing a stream.
func TestExtractBinaryTakesOnlyMediamtx(t *testing.T) {
	archive := makeTarball(t, map[string]string{
		"LICENSE":      "лицензия",
		"mediamtx.yml": "paths:\n  all:\n",
		"mediamtx":     "это бинарник",
	})

	dir := t.TempDir()
	dest := filepath.Join(dir, "mediamtx")
	if err := extractBinary(archive, dest); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "это бинарник" {
		t.Fatalf("распакован не тот файл: %q", got)
	}

	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("права %o: mediamtx не запустится", st.Mode().Perm())
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("в каталоге %d файлов, ожидался один mediamtx", len(left))
	}
}

// TestExtractBinaryIgnoresPathsInsideArchive: only the base name is matched, so
// an entry spelled with a path cannot write anywhere but the destination. The
// published archive has no such entry; a replaced or repacked one could.
func TestExtractBinaryIgnoresPathsInsideArchive(t *testing.T) {
	archive := makeTarball(t, map[string]string{
		"../../etc/cron.d/mediamtx": "побег из каталога",
	})

	dest := filepath.Join(t.TempDir(), "mediamtx")
	if err := extractBinary(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("файл не на месте назначения: %v", err)
	}
	if string(got) != "побег из каталога" {
		t.Fatalf("содержимое %q", got)
	}
}

// TestExtractBinaryRejectsArchiveWithoutIt: a release whose layout changed must
// fail loudly rather than leave an empty file that the checksum check would
// then blame on a corrupt download.
func TestExtractBinaryRejectsArchiveWithoutIt(t *testing.T) {
	archive := makeTarball(t, map[string]string{"README.md": "нет бинарника"})

	dest := filepath.Join(t.TempDir(), "mediamtx")
	err := extractBinary(archive, dest)
	if err == nil {
		t.Fatal("архив без mediamtx принят")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("после отказа остался файл назначения")
	}
}
