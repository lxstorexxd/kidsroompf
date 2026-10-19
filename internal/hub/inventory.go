// Package hub manages a fleet of kidsroom agents from one portable binary. It
// holds the inventory of sites, talks to their control APIs and renders the
// results. Nothing here listens on a socket: the hub runs, does what it was
// asked and exits.
package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// InventoryVersion is the on-disk format version, so a future change can be
// recognised instead of guessed at.
//
// Version 1 held only how to reach a site. Version 2 adds the settings each
// site should have, which makes the file the source of truth: a site that has
// to be reinstalled is restored from here instead of from memory.
const InventoryVersion = 2

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Site is one agent: where it answers, what it is called, and what it should be
// configured with.
type Site struct {
	Name  string   `json:"name"`
	Addr  string   `json:"addr"`
	Token string   `json:"token"`
	Tags  []string `json:"tags,omitempty"`
	Note  string   `json:"note,omitempty"`
	// Fingerprint pins the SHA-256 of the agent's TLS certificate. It is
	// optional: empty means the certificate is not checked, which is the usual
	// case when an agent is reached by its VPN address while its certificate
	// names a public domain.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Settings is the configuration this site should have — the same document
	// the agent's API takes, kept as a JSON tree rather than a typed struct so
	// that a site running an agent of another version keeps the fields this
	// build has never heard of.
	//
	// It is what push sends and what pull fills in. Absent means "nothing
	// recorded yet": push has nothing to do, pull will record what is there.
	Settings Doc `json:"settings,omitempty"`
}

// clone returns a deep copy, so editing one site cannot reach into the stored
// inventory through a shared map or slice.
func (s Site) clone() Site {
	cp := s
	cp.Tags = append([]string(nil), s.Tags...)
	cp.Settings = s.Settings.clone()
	return cp
}

// CameraCount reports how many cameras the recorded settings describe, and how
// many of them are switched on.
func (s Site) CameraCount() (enabled, total int) {
	cams, err := s.Settings.Cameras()
	if err != nil {
		return 0, 0
	}
	for _, c := range cams {
		total++
		if on, ok := c["enabled"].(bool); ok && on {
			enabled++
		}
	}
	return enabled, total
}

// HasTag reports whether the site carries the tag.
func (s Site) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// Validate checks a site before it enters the inventory, so a typo surfaces at
// add time rather than in the middle of a fleet-wide change.
func (s Site) Validate() error {
	var probs []string

	if !nameRe.MatchString(s.Name) {
		probs = append(probs, fmt.Sprintf("имя %q: нужны строчные латинские буквы, цифры, _ и -", s.Name))
	}

	u, err := url.Parse(s.Addr)
	switch {
	case err != nil:
		probs = append(probs, "адрес не разобрать: "+err.Error())
	case u.Scheme != "http" && u.Scheme != "https":
		probs = append(probs, "адрес должен начинаться с http:// или https://")
	case u.Host == "":
		probs = append(probs, "в адресе нет хоста")
	case u.Path != "" && u.Path != "/":
		probs = append(probs, "адрес должен быть без пути: только схема, хост и порт")
	}

	// An empty token is not rejected here: while a fleet is being provisioned the
	// inventory legitimately holds sites whose token has not been collected yet.
	// A request without one fails with a message that says exactly that, and add
	// — the one command that registers a site — proves the token by connecting.
	for _, t := range s.Tags {
		if !nameRe.MatchString(t) {
			probs = append(probs, fmt.Sprintf("метка %q: нужны строчные латинские буквы, цифры, _ и -", t))
		}
	}
	if err := s.Settings.validate(); err != nil {
		probs = append(probs, "settings: "+err.Error())
	}

	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

// Inventory is the whole fleet as stored on disk.
type Inventory struct {
	Version int    `json:"version"`
	Sites   []Site `json:"sites"`

	path string
}

// DefaultPath is inventory.json next to the binary, overridden by
// KIDSROOM_HUB_INVENTORY. Keeping it beside the executable is what makes the
// hub portable: binary and inventory travel together.
func DefaultPath() string {
	if v := strings.TrimSpace(os.Getenv("KIDSROOM_HUB_INVENTORY")); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return "inventory.json"
	}
	return filepath.Join(filepath.Dir(exe), "inventory.json")
}

// Load reads the inventory. A missing file yields an empty one, so add works on
// a fresh machine without a setup step.
func Load(path string) (*Inventory, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Inventory{Version: InventoryVersion, path: path}, nil
	}
	if err != nil {
		return nil, err
	}
	// Notepad and PowerShell's Out-File both leave a UTF-8 BOM in front, and the
	// JSON parser would otherwise refuse the file over a character nobody typed.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	// Creating inventory.json by hand before running add leaves an empty file,
	// and an empty file is not valid JSON — so the file would block the very
	// command meant to fill it. It plainly means "no sites yet".
	if len(bytes.TrimSpace(raw)) == 0 {
		return &Inventory{Version: InventoryVersion, path: path}, nil
	}

	inv := &Inventory{path: path}
	if err := json.Unmarshal(raw, inv); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", path, err)
	}
	if inv.Version > InventoryVersion {
		return nil, fmt.Errorf("%s написан версией формата %d, эта программа понимает %d",
			path, inv.Version, InventoryVersion)
	}
	// Version 1 knew nothing about settings, so there is nothing to convert: the
	// sites simply have none recorded yet, and pull fills them in.
	inv.Version = InventoryVersion

	seen := map[string]bool{}
	for _, s := range inv.Sites {
		key := strings.ToLower(s.Name)
		if seen[key] {
			return nil, fmt.Errorf("%s: площадка %q указана дважды", path, s.Name)
		}
		seen[key] = true
	}
	return inv, nil
}

// Path is the file this inventory came from.
func (inv *Inventory) Path() string { return inv.path }

// Save writes the inventory atomically: the hub rewrites it after rotating a
// token, and a half-written file would cost access to the whole fleet.
func (inv *Inventory) Save() error {
	if inv.path == "" {
		return errors.New("не задан путь к инвентарю")
	}
	data, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(inv.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".inventory-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, inv.path)
}

// Find returns a copy of the site by name. It is a copy so that a caller which
// edits settings cannot change the stored inventory by accident — only Update
// writes back.
func (inv *Inventory) Find(name string) (Site, bool) {
	for _, s := range inv.Sites {
		if strings.EqualFold(s.Name, name) {
			return s.clone(), true
		}
	}
	return Site{}, false
}

// Add appends a validated site.
func (inv *Inventory) Add(s Site) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, ok := inv.Find(s.Name); ok {
		return fmt.Errorf("площадка %q уже есть в инвентаре", s.Name)
	}
	inv.Sites = append(inv.Sites, s.clone())
	return nil
}

// Remove drops a site by name.
func (inv *Inventory) Remove(name string) error {
	for i, s := range inv.Sites {
		if strings.EqualFold(s.Name, name) {
			inv.Sites = append(inv.Sites[:i], inv.Sites[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("площадка %q не найдена в инвентаре", name)
}

// Update replaces a site in place, keeping its position in the file.
func (inv *Inventory) Update(s Site) error {
	if err := s.Validate(); err != nil {
		return err
	}
	for i := range inv.Sites {
		if strings.EqualFold(inv.Sites[i].Name, s.Name) {
			inv.Sites[i] = s.clone()
			return nil
		}
	}
	return fmt.Errorf("площадка %q не найдена в инвентаре", s.Name)
}

// Selection says which sites a command applies to.
type Selection struct {
	Names []string
	Tags  []string
	All   bool
}

// Resolve turns a selection into sites, in inventory order. An unknown name or
// a tag that matches nothing is an error: silently doing less than asked is how
// a certificate quietly fails to reach half the fleet.
func (inv *Inventory) Resolve(sel Selection) ([]Site, error) {
	if len(inv.Sites) == 0 {
		return nil, fmt.Errorf("инвентарь пуст: заведите площадку командой add (файл %s)", inv.path)
	}

	switch {
	case sel.All:
		if len(sel.Names) > 0 || len(sel.Tags) > 0 {
			return nil, errors.New("--all нельзя сочетать с именами площадок или --tag")
		}
		out := make([]Site, 0, len(inv.Sites))
		for _, s := range inv.Sites {
			out = append(out, s.clone())
		}
		return out, nil
	case len(sel.Names) == 0 && len(sel.Tags) == 0:
		return nil, errors.New("не указано, к каким площадкам применить: перечислите имена, либо --all, либо --tag")
	}

	want := map[string]bool{}
	for _, n := range sel.Names {
		s, ok := inv.Find(n)
		if !ok {
			return nil, fmt.Errorf("площадка %q не найдена в инвентаре", n)
		}
		want[s.Name] = true
	}
	for _, tag := range sel.Tags {
		found := false
		for _, s := range inv.Sites {
			if s.HasTag(tag) {
				want[s.Name] = true
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("метка %q не стоит ни на одной площадке", tag)
		}
	}

	out := make([]Site, 0, len(want))
	for _, s := range inv.Sites {
		if want[s.Name] {
			out = append(out, s.clone())
		}
	}
	return out, nil
}
