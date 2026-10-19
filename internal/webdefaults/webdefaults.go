// Package webdefaults carries the factory copy of the player page. It is
// embedded only as a seed: the served page always comes from disk, so the
// operator can edit it and keep those edits across agent updates.
package webdefaults

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

//go:embed all:default
var factory embed.FS

// Install writes the factory page into dir for every file that is missing.
// Existing files are never touched, so hand edits survive.
func Install(dir string, logf func(string, ...any)) error {
	return extract(dir, false, logf)
}

// Reset restores the factory page, moving the current directory aside to
// web.bak-<timestamp> first.
func Reset(dir string, logf func(string, ...any)) (backup string, err error) {
	if _, err := os.Stat(dir); err == nil {
		backup = fmt.Sprintf("%s.bak-%s", dir, time.Now().Format("20060102-150405"))
		if err := os.Rename(dir, backup); err != nil {
			return "", fmt.Errorf("не удалось отложить %s: %w", dir, err)
		}
		logf("прежний каталог страницы сохранён как %s", backup)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return backup, extract(dir, true, logf)
}

func extract(dir string, overwrite bool, logf func(string, ...any)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	written := 0

	err := fs.WalkDir(factory, "default", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("default", p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(dir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if !overwrite {
			if _, err := os.Stat(dest); err == nil {
				return nil // already there, leave the operator's version alone
			}
		}
		if err := copyOut(p, dest); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return err
	}

	if written > 0 {
		logf("в %s записано файлов заводской страницы: %d", dir, written)
	}
	return nil
}

func copyOut(embedded, dest string) error {
	src, err := factory.Open(embedded)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	return out.Sync()
}
