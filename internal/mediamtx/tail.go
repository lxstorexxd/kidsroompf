package mediamtx

import (
	"strings"
	"sync"
)

// tail keeps the last few lines a process printed, so a start-up failure can
// be reported with the process's own words. mediamtx writes the real reason —
// an unknown configuration key, a busy port — to its log and exits with
// status 1, which on its own tells nobody anything.
type tail struct {
	mu    sync.Mutex
	lines []string
	max   int
	buf   strings.Builder
}

func newTail(max int) *tail {
	return &tail{max: max}
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf.Write(p)
	text := t.buf.String()

	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		t.push(strings.TrimRight(text[:i], "\r"))
		text = text[i+1:]
	}

	t.buf.Reset()
	// A partial line is kept for the next write, but a runaway line without a
	// newline must not grow without bound.
	if len(text) > 4096 {
		text = text[len(text)-4096:]
	}
	t.buf.WriteString(text)
	return len(p), nil
}

func (t *tail) push(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

// lastError returns the most useful line for a failure report: the last one
// mediamtx logged at error level, or simply the last line it printed.
func (t *tail) lastError() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i := len(t.lines) - 1; i >= 0; i-- {
		if strings.Contains(t.lines[i], " ERR ") || strings.Contains(t.lines[i], "ERR:") {
			return trimTimestamp(t.lines[i])
		}
	}
	if len(t.lines) > 0 {
		return trimTimestamp(t.lines[len(t.lines)-1])
	}
	return ""
}

// trimTimestamp drops the date and time mediamtx prefixes to every line; the
// agent adds its own when it logs the message.
func trimTimestamp(line string) string {
	fields := strings.SplitN(line, " ", 3)
	if len(fields) == 3 && strings.Count(fields[0], "/") == 2 && strings.Count(fields[1], ":") == 2 {
		return fields[2]
	}
	return line
}
