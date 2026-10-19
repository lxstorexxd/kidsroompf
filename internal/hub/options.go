package hub

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Options are the flags shared by every command.
type Options struct {
	InventoryPath string
	Timeout       time.Duration
	Parallel      int
	JSON          bool
	Yes           bool
	DryRun        bool
	StopOnError   bool

	Out io.Writer
	Err io.Writer
	In  io.Reader
}

// DefaultOptions wires the standard streams and the read-path defaults.
func DefaultOptions() *Options {
	return &Options{
		InventoryPath: DefaultPath(),
		Timeout:       10 * time.Second,
		Parallel:      8,
		Out:           os.Stdout,
		Err:           os.Stderr,
		In:            os.Stdin,
	}
}

func (o *Options) inventory() (*Inventory, error) {
	return Load(o.InventoryPath)
}

func (o *Options) client(s Site) *Client { return NewClient(s, o.Timeout) }

// writeParallel is the concurrency for commands that change something. It is
// lower than for reads on purpose: a bad change should be visible after a few
// sites, not after all fifty have already taken it.
func (o *Options) writeParallel() int {
	if o.Parallel > 4 {
		return 4
	}
	return o.Parallel
}

// confirm asks before changing more than one site. Nobody means to retune fifty
// nurseries by a mistyped flag, and the list of names is the last place to
// notice it.
func (o *Options) confirm(sites []Site, what string) error {
	if o.Yes || o.DryRun || len(sites) <= 1 {
		return nil
	}

	names := make([]string, 0, len(sites))
	for _, s := range sites {
		names = append(names, s.Name)
	}
	fmt.Fprintf(o.Out, "%s\nплощадок: %d — %s\n", what, len(sites), strings.Join(names, ", "))

	if !interactive(o.In) {
		return errors.New("вывод не на терминал: добавьте --yes, чтобы подтвердить изменение, или --dry-run, чтобы посмотреть, что будет отправлено")
	}

	fmt.Fprint(o.Out, "Продолжить? [y/N] ")
	line, err := bufio.NewReader(o.In).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("подтверждение не получено")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "д", "да":
		return nil
	}
	return errors.New("отменено")
}

// interactive reports whether a prompt can actually be answered.
func interactive(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// dryRun prints what would be sent and reports whether the command should stop
// here.
func (o *Options) dryRun(sites []Site, body []byte) bool {
	if !o.DryRun {
		return false
	}
	names := make([]string, 0, len(sites))
	for _, s := range sites {
		names = append(names, s.Name)
	}
	fmt.Fprintf(o.Out, "--dry-run: ничего не отправлено\nплощадки (%d): %s\nтело запроса:\n%s\n",
		len(sites), strings.Join(names, ", "), body)
	return true
}
