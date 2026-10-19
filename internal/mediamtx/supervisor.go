package mediamtx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/paths"
)

// Supervisor owns the mediamtx child process: it writes the config, keeps the
// process alive and reports stream health.
type Supervisor struct {
	configPath string
	exePath    string
	logw       io.Writer
	logf       func(format string, args ...any)

	mu          sync.Mutex
	cfg         *config.Config
	binVersion  string
	cmd         *exec.Cmd
	intentional bool // the current kill was requested by us, so skip backoff
	startedAt   time.Time
	restarts    int
	lastErr     string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// restartCh wakes the loop out of its back-off pause, so a fix applied by
	// hand takes effect at once instead of after up to half a minute.
	restartCh chan struct{}
	api       *http.Client
}

// New builds a supervisor. logw receives the raw mediamtx output, logf the
// agent's own notes about the process lifecycle.
func New(cfg *config.Config, logw io.Writer, logf func(string, ...any)) *Supervisor {
	return &Supervisor{
		configPath: paths.MediamtxConfigFile(),
		exePath:    paths.MediamtxBinary(),
		logw:       logw,
		logf:       logf,
		cfg:        cfg,
		done:       make(chan struct{}),
		restartCh:  make(chan struct{}, 1),
		api:        &http.Client{Timeout: 3 * time.Second},
	}
}

// Start writes the config and launches the supervising loop.
func (s *Supervisor) Start(ctx context.Context) error {
	if _, err := os.Stat(s.exePath); err != nil {
		return fmt.Errorf("не найден %s: запустите "+paths.AgentCmd+" fetch-mediamtx", s.exePath)
	}
	s.checkVersion()
	if err := WriteConfig(s.configPath, s.snapshot()); err != nil {
		return err
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	go s.loop()
	return nil
}

func (s *Supervisor) snapshot() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Apply regenerates the config and restarts mediamtx so the new camera set
// takes effect. The public TLS listener is untouched.
func (s *Supervisor) Apply(cfg *config.Config) error {
	s.mu.Lock()
	s.cfg = cfg.Clone()
	s.mu.Unlock()

	if err := WriteConfig(s.configPath, cfg); err != nil {
		return err
	}
	s.Restart()
	return nil
}

// Restart kills the child; the loop starts it again immediately.
func (s *Supervisor) Restart() {
	// The binary may have been replaced since the last start — that is exactly
	// what fetch-mediamtx does to fix a version mismatch.
	s.checkVersion()

	s.mu.Lock()
	cmd := s.cmd
	s.intentional = true
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// If the loop is waiting out a back-off instead of running the process,
	// killing nothing would leave it asleep; this wakes it.
	select {
	case s.restartCh <- struct{}{}:
	default:
	}
}

// Stop terminates the child and waits for the loop to finish.
func (s *Supervisor) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
}

func (s *Supervisor) loop() {
	defer close(s.done)

	backoff := time.Second
	for {
		if s.ctx.Err() != nil {
			return
		}

		err := s.runOnce()

		s.mu.Lock()
		intentional := s.intentional
		s.intentional = false
		s.restarts++
		if err != nil && !intentional {
			s.lastErr = err.Error()
		} else if intentional {
			s.lastErr = ""
		}
		s.mu.Unlock()

		if s.ctx.Err() != nil {
			return
		}

		if intentional {
			s.logf("mediamtx перезапускается по запросу")
			// The wake-up signal has done its job; drop it so it cannot fire
			// again on the next pause.
			select {
			case <-s.restartCh:
			default:
			}
			backoff = time.Second
			continue
		}

		s.logf("mediamtx завершился (%v), перезапуск через %s", err, backoff)
		select {
		case <-s.ctx.Done():
			return
		case <-s.restartCh:
			// Someone asked for a restart while we were waiting: go now and
			// start counting from scratch.
			backoff = time.Second
			continue
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// runOnce starts mediamtx and blocks until it exits.
func (s *Supervisor) runOnce() error {
	// The last lines are kept so that a startup failure can be reported with
	// mediamtx's own words instead of a bare "exit status 1".
	tail := newTail(5)
	out := io.MultiWriter(s.logw, tail)

	cmd := exec.Command(s.exePath, s.configPath)
	cmd.Dir = paths.WorkDir()
	cmd.Stdout = out
	cmd.Stderr = out
	applyProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск mediamtx: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.startedAt = time.Now()
	s.mu.Unlock()
	s.logf("mediamtx запущен, pid %d", cmd.Process.Pid)

	err := cmd.Wait()

	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()

	if err != nil {
		if last := tail.lastError(); last != "" {
			return fmt.Errorf("%w: %s", err, last)
		}
	}
	return err
}

// checkVersion compares the binary next to the agent with the version the
// generated configuration is written for. A mismatch is the one failure that
// looks like nothing at all: an older mediamtx rejects keys it does not know
// and exits, and the agent would restart it forever.
func (s *Supervisor) checkVersion() {
	out, err := exec.Command(s.exePath, "--version").Output()
	if err != nil {
		s.logf("не удалось определить версию mediamtx: %v", err)
		return
	}
	got := strings.TrimSpace(string(out))

	s.mu.Lock()
	s.binVersion = got
	s.mu.Unlock()

	if got != Version {
		s.logf("ВНИМАНИЕ: рядом с агентом mediamtx %s, а настройки пишутся под %s. "+
			"Старая версия не понимает часть параметров и откажется стартовать. "+
			"Выполните: "+paths.AgentCmd+" fetch-mediamtx", got, Version)
	}
}

// BinaryVersion reports the version of the mediamtx actually in use.
func (s *Supervisor) BinaryVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binVersion
}

// PathStatus is the per-camera health shown in the tray and the GUI.
type PathStatus struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Readers int    `json:"readers"`
	Source  string `json:"source"`
	Error   string `json:"error,omitempty"`
}

// Status summarises the child process and its paths.
type Status struct {
	Running   bool         `json:"running"`
	PID       int          `json:"pid"`
	Uptime    string       `json:"uptime"`
	Restarts  int          `json:"restarts"`
	LastError string       `json:"last_error,omitempty"`
	Paths     []PathStatus `json:"paths"`
}

// Status reports the current state, asking mediamtx about its paths.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	cmd := s.cmd
	st := Status{
		Running:   cmd != nil,
		Restarts:  s.restarts,
		LastError: s.lastErr,
	}
	if cmd != nil && cmd.Process != nil {
		st.PID = cmd.Process.Pid
		st.Uptime = time.Since(s.startedAt).Round(time.Second).String()
	}
	s.mu.Unlock()

	if !st.Running {
		return st
	}
	list, err := s.pathList()
	if err != nil {
		if st.LastError == "" {
			st.LastError = "mediamtx API недоступен: " + err.Error()
		}
		return st
	}
	st.Paths = list
	return st
}

type apiPathList struct {
	Items []struct {
		Name   string `json:"name"`
		Ready  bool   `json:"ready"`
		Source *struct {
			Type string `json:"type"`
		} `json:"source"`
		Readers []struct {
			Type string `json:"type"`
		} `json:"readers"`
	} `json:"items"`
}

func (s *Supervisor) pathList() ([]PathStatus, error) {
	resp, err := s.api.Get(APIBase(s.snapshot()) + "/v3/paths/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("код ответа %d", resp.StatusCode)
	}
	var parsed apiPathList
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]PathStatus, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		ps := PathStatus{Name: it.Name, Ready: it.Ready, Readers: len(it.Readers)}
		if it.Source != nil {
			ps.Source = it.Source.Type
		}
		out = append(out, ps)
	}
	return out, nil
}

// Probe asks mediamtx whether one path is currently pulling video. It is what
// the GUI "Проверить" button reports after a settings change.
func (s *Supervisor) Probe(id string) (PathStatus, error) {
	list, err := s.pathList()
	if err != nil {
		return PathStatus{}, err
	}
	for _, p := range list {
		if p.Name == id {
			return p, nil
		}
	}
	return PathStatus{Name: id}, errors.New("путь не найден в mediamtx: камера выключена или настройки не применены")
}
