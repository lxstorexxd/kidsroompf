// Package logx provides a dependency-free size-rotating log file, used for the
// agent's own log and for the captured mediamtx output.
package logx

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	defaultMaxSize = 5 << 20 // rotate at 5 MiB
	defaultKeep    = 3       // name.log + name.log.1 .. name.log.3
)

// Writer is an io.WriteCloser that rotates the file once it grows past
// maxSize, keeping at most keep old generations. Disk use is therefore capped
// at (keep + 1) * maxSize — 20 MiB with the defaults. It is safe for
// concurrent use.
type Writer struct {
	path    string
	maxSize int64
	keep    int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// New opens (creating the directory if needed) the log file for appending.
func New(path string) (*Writer, error) {
	return newWriter(path, defaultMaxSize, defaultKeep)
}

func newWriter(path string, maxSize int64, keep int) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &Writer{path: path, maxSize: maxSize, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	// A single write larger than the limit would otherwise rotate on every
	// call and still never fit, so an empty file always takes what it is given.
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts name.log.N upwards and starts a fresh file. Errors are ignored
// on purpose: losing log history must never stop the stream.
func (w *Writer) rotate() {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	_ = os.Remove(w.path + "." + strconv.Itoa(w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(w.path+"."+strconv.Itoa(i), w.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	_ = w.open()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
