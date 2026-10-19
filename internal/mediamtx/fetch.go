package mediamtx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"kidsroom-agent/internal/paths"
)

// Version is the pinned release the agent is tested against. The generated
// mediamtx.yml is written for exactly this version: an older binary rejects
// keys it does not know and exits at once.
const Version = "v1.21.2"

// Fetch downloads the pinned mediamtx release and installs the binary where
// the agent expects it. It extracts that one file and nothing else.
//
// Both checksums are checked: the archive, so a tampered or truncated download
// is refused before anything is unpacked, and the binary itself, so what ends
// up on disk is byte for byte what this build was tested with.
func Fetch(progress func(string, ...any)) error {
	target := paths.MediamtxBinary()
	if sum, err := fileSHA256(target); err == nil && sum == binarySHA256 {
		progress("mediamtx %s уже установлен: %s", Version, target)
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "kidsroom-mtx-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archive := filepath.Join(tmpDir, filepath.Base(downloadURL))
	progress("Скачиваю %s", downloadURL)
	if err := download(downloadURL, archive); err != nil {
		return err
	}

	sum, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	if sum != archiveSHA256 {
		return fmt.Errorf("контрольная сумма архива не совпала: получено %s, ожидалось %s", sum, archiveSHA256)
	}
	progress("Контрольная сумма архива совпала")

	extracted := filepath.Join(tmpDir, paths.MediamtxName)
	if err := extractBinary(archive, extracted); err != nil {
		return err
	}
	if sum, err := fileSHA256(extracted); err != nil {
		return err
	} else if sum != binarySHA256 {
		return fmt.Errorf("контрольная сумма %s не совпала: получено %s", paths.MediamtxName, sum)
	}

	if err := replaceFile(extracted, target); err != nil {
		return err
	}
	progress("Установлен %s (%s)", target, Version)
	return nil
}

func download(url, dest string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("скачивание %s: код ответа %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// writeBinary copies one archive entry out to dest, executable.
//
// The archive is pinned by checksum, so the size is known-good; the limit is
// only a guard against a surprise 1 GiB entry.
func writeBinary(src io.Reader, dest string) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(src, 512<<20)); err != nil {
		return err
	}
	return out.Sync()
}

// replaceFile moves the new binary into place, stepping around a running copy
// by renaming the old one out of the way first.
func replaceFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		backup := dest + ".old"
		_ = os.Remove(backup)
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("не удалось подвинуть старый %s (остановите службу): %w", dest, err)
		}
		defer os.Remove(backup)
	}
	if err := os.Rename(src, dest); err != nil {
		// A rename across volumes fails; fall back to a copy.
		return copyFile(src, dest)
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeBinary(in, dest)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
