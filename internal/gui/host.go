//go:build windows

package gui

import (
	"runtime"
	"sync"

	"github.com/lxn/walk"
)

// Host owns the settings window inside the tray process.
//
// A Win32 window belongs to the thread that created it and only lives while
// that thread pumps messages, so the window gets a dedicated locked thread of
// its own. The tray icon has its own loop on another thread; the two never
// share one.
//
// The window is created on first use and then kept: closing it only hides it,
// which is why its message loop keeps running and Show can simply bring it
// back.
type Host struct {
	show chan struct{}
	quit chan struct{}
	done chan struct{}

	mu       sync.Mutex
	w        *window
	quitting bool
}

func NewHost() *Host {
	return &Host{
		show: make(chan struct{}, 1),
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start launches the UI thread. It returns immediately; the window appears
// only when Show is called.
func (h *Host) Start() {
	go h.loop()
}

// Show opens the settings window, creating it the first time and refreshing it
// on every later call.
func (h *Host) Show() {
	h.mu.Lock()
	w := h.w
	h.mu.Unlock()

	if w == nil {
		// No window yet: wake the UI thread, which will build one.
		select {
		case h.show <- struct{}{}:
		default: // a request is already queued
		}
		return
	}

	w.mw.Synchronize(func() {
		// Settings may have changed through the API since the window was last
		// open, so what it shows is refreshed every time.
		w.refresh()
		w.mw.SetVisible(true)
		w.restoreIfMinimized()
		// A hidden window gets no layout pass, so the children can be sized
		// for the geometry it had when it was hidden. Laying it out again on
		// the way back prevents the tab pages from drawing over each other.
		w.mw.RequestLayout()
		w.mw.Invalidate()
		win32BringToFront(w.mw)
	})
}

// Quit closes the window for good and lets the UI thread finish.
func (h *Host) Quit() {
	h.mu.Lock()
	h.quitting = true
	w := h.w
	h.mu.Unlock()

	if w != nil {
		w.mw.Synchronize(func() { _ = w.mw.Close() })
	}
	close(h.quit)
	<-h.done
}

func (h *Host) loop() {
	// The window and its message pump must stay on one and the same thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(h.done)

	for {
		select {
		case <-h.quit:
			return
		case <-h.show:
			h.runWindow()
			h.mu.Lock()
			quitting := h.quitting
			h.mu.Unlock()
			if quitting {
				return
			}
		}
	}
}

// runWindow builds the window and pumps its messages until the agent quits.
// A failure to build (service down, for example) is reported to the user and
// leaves the tray running.
func (h *Host) runWindow() {
	w, err := newWindow()
	if err != nil {
		reportStartupError(err)
		return
	}

	w.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		h.mu.Lock()
		quitting := h.quitting
		h.mu.Unlock()
		if quitting {
			return
		}
		// Closing the window only puts the agent back in the tray; it keeps
		// running, and so does the stream.
		*canceled = true
		w.mw.SetVisible(false)
	})

	h.mu.Lock()
	h.w = w
	h.mu.Unlock()

	w.mw.SetVisible(true)
	win32BringToFront(w.mw)
	w.mw.Run()

	h.mu.Lock()
	h.w = nil
	h.mu.Unlock()
}
