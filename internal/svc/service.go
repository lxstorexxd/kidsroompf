// Package svc runs the agent as a service and installs everything that belongs
// to it.
//
// What that includes depends on the platform: on Windows the service entry, the
// firewall rules and the tray autostart; on Linux a systemd unit, the
// directories under /etc and /var, and the binaries under /usr/local. The parts
// that differ live in platform_windows.go and platform_linux.go, so the order
// of the steps is written once.
package svc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kardianos/service"

	"kidsroom-agent/internal/app"
	"kidsroom-agent/internal/paths"
)

// Name is the service name.
const Name = paths.AppName

type program struct {
	cancel context.CancelFunc
	done   chan struct{}
	// interactive is true when started from a console, which makes the agent
	// mirror its log to stderr.
	interactive bool
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})

	a, err := app.New(p.interactive)
	if err != nil {
		cancel()
		return err
	}
	go func() {
		defer close(p.done)
		if err := a.Run(ctx); err != nil {
			if logger, lerr := s.Logger(nil); lerr == nil {
				_ = logger.Error(err.Error())
			}
			fmt.Fprintln(os.Stderr, "ошибка:", err)
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		<-p.done
	}
	return nil
}

func serviceConfig() *service.Config {
	return &service.Config{
		Name:        Name,
		DisplayName: "Kidsroom Agent — трансляция детской",
		Description: "Публикует трансляцию камер детской комнаты по HTTPS и управляет mediamtx.",
		Arguments:   []string{"run"},
		// Both are empty on Windows, where the service manager runs the binary
		// from wherever it was unpacked and keeps its own working directory.
		Executable:       executablePath(),
		WorkingDirectory: workingDirectory(),
		Option:           serviceOptions(),
	}
}

func newService() (service.Service, *program, error) {
	prg := &program{interactive: service.Interactive()}
	s, err := service.New(prg, serviceConfig())
	if err != nil {
		return nil, nil, err
	}
	return s, prg, nil
}

// Run executes the agent. Under the SCM it behaves as a service; from a console
// it runs in the foreground until Ctrl+C.
func Run() error {
	s, _, err := newService()
	if err != nil {
		return err
	}
	return s.Run()
}

// Install registers the service, does whatever else the platform needs and
// starts it. Prepare must have run first.
func Install(logf func(string, ...any)) error {
	s, _, err := newService()
	if err != nil {
		return err
	}
	if err := s.Install(); err != nil {
		// The library reports this one in English, and it is the error an
		// operator is most likely to hit — a second install over the first.
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("служба уже установлена: сначала %s uninstall, затем install", paths.AgentCmd)
		}
		return fmt.Errorf("установка службы: %w", err)
	}
	logf("служба %s установлена", Name)

	afterInstall(logf)

	if err := s.Start(); err != nil {
		return fmt.Errorf("запуск службы: %w", err)
	}
	logf("служба запущена")
	return nil
}

// Uninstall stops the service and removes everything Install added.
func Uninstall(logf func(string, ...any)) error {
	s, _, err := newService()
	if err != nil {
		return err
	}
	if err := s.Stop(); err != nil {
		logf("служба не остановлена (возможно, уже стоит): %v", err)
	}
	beforeUninstall(logf)
	if err := s.Uninstall(); err != nil {
		return fmt.Errorf("удаление службы: %w", err)
	}
	logf("служба %s удалена", Name)
	return nil
}

// Control runs start, stop or restart against the installed service.
func Control(action string) error {
	s, _, err := newService()
	if err != nil {
		return err
	}
	return service.Control(s, action)
}

// Status reports whether the service is running.
func Status() (service.Status, error) {
	s, _, err := newService()
	if err != nil {
		return service.StatusUnknown, err
	}
	return s.Status()
}

// State describes the Windows service as the tray needs to see it.
type State int

const (
	StateUnknown State = iota
	StateNotInstalled
	StateStopped
	StateRunning
)

func (s State) String() string {
	switch s {
	case StateNotInstalled:
		return "не установлена"
	case StateStopped:
		return "остановлена"
	case StateRunning:
		return "работает"
	default:
		return "состояние неизвестно"
	}
}

// CurrentState reports whether the service exists and whether it runs. The two
// cases need different fixes, so they are never collapsed into one.
func CurrentState() State {
	s, _, err := newService()
	if err != nil {
		return StateUnknown
	}
	status, err := s.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		return StateNotInstalled
	case err != nil:
		return StateUnknown
	case status == service.StatusRunning:
		return StateRunning
	case status == service.StatusStopped:
		return StateStopped
	default:
		return StateUnknown
	}
}

// Installed reports whether the Windows service entry exists.
func Installed() bool {
	return CurrentState() != StateNotInstalled && CurrentState() != StateUnknown
}
