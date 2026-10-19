package mediamtx

import "testing"

// A failed start must be reported with mediamtx's own words. Without this the
// agent would only say "exit status 1" and restart forever, which is what an
// older mediamtx rejecting an unknown configuration key looks like.
func TestTailPrefersErrorLine(t *testing.T) {
	tl := newTail(5)
	tl.Write([]byte("2026/10/09 13:01:35 INF MediaMTX v1.13.1, windows, amd64\n"))
	tl.Write([]byte("2026/10/09 13:01:35 ERR: json: unknown field \"hlsAllowOrigins\"\n"))

	if got, want := tl.lastError(), `ERR: json: unknown field "hlsAllowOrigins"`; got != want {
		t.Errorf("получено %q, ожидалось %q", got, want)
	}
}

func TestTailFallsBackToLastLine(t *testing.T) {
	tl := newTail(5)
	tl.Write([]byte("2026/10/09 13:01:35 INF первая\n2026/10/09 13:01:36 INF последняя\n"))

	if got, want := tl.lastError(), "INF последняя"; got != want {
		t.Errorf("получено %q, ожидалось %q", got, want)
	}
}

// Output arrives in whatever chunks the pipe delivers, not in whole lines.
func TestTailReassemblesSplitWrites(t *testing.T) {
	tl := newTail(5)
	tl.Write([]byte("2026/10/09 13:01:35 ERR listen tcp 127.0.0.1:8888"))
	tl.Write([]byte(": bind: address already in use\n"))

	if got, want := tl.lastError(), "ERR listen tcp 127.0.0.1:8888: bind: address already in use"; got != want {
		t.Errorf("получено %q, ожидалось %q", got, want)
	}
}

func TestTailKeepsOnlyRecentLines(t *testing.T) {
	tl := newTail(2)
	for _, s := range []string{"INF один\n", "INF два\n", "INF три\n"} {
		tl.Write([]byte("2026/10/09 13:01:35 " + s))
	}

	tl.mu.Lock()
	n := len(tl.lines)
	tl.mu.Unlock()
	if n != 2 {
		t.Errorf("хранится строк: %d, ожидалось 2", n)
	}
	if got := tl.lastError(); got != "INF три" {
		t.Errorf("получено %q, ожидалась последняя строка", got)
	}
}

func TestTailIgnoresEmptyOutput(t *testing.T) {
	tl := newTail(3)
	tl.Write([]byte("\n   \n\n"))

	if got := tl.lastError(); got != "" {
		t.Errorf("из пустого вывода получено %q", got)
	}
}
