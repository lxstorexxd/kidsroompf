package hub

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"kidsroom-agent/internal/certs"
)

// Mask is what the agent sends instead of a secret, and what it accepts back to
// mean "keep the stored value". It mirrors config.Mask; the constant is
// repeated rather than imported because internal/config is Windows-only and the
// hub has to build for any machine the operator happens to have.
const Mask = "***"

// Doc is a settings document as a JSON tree. The hub edits trees rather than a
// typed struct so that a field this build has never heard of survives a
// read-modify-write against an agent of a different version.
type Doc map[string]any

// Marshal renders a document for sending.
func (d Doc) Marshal() ([]byte, error) { return json.Marshal(d) }

// Section returns a nested object, creating it if absent.
func (d Doc) Section(name string) Doc {
	if cur, ok := d[name].(map[string]any); ok {
		return Doc(cur)
	}
	sub := Doc{}
	d[name] = map[string]any(sub)
	return sub
}

// Cameras reads the camera list out of a settings document.
func (d Doc) Cameras() ([]Doc, error) {
	raw, ok := d["cameras"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("в настройках площадки поле cameras не является списком")
	}
	out := make([]Doc, 0, len(list))
	for i, item := range list {
		cam, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("в настройках площадки cameras[%d] не является объектом", i)
		}
		out = append(out, Doc(cam))
	}
	return out, nil
}

// String reads a string field, returning "" when it is absent or of another
// type.
func (d Doc) String(key string) string {
	s, _ := d[key].(string)
	return s
}

// findCamera locates a camera by id.
func findCamera(cams []Doc, id string) (int, bool) {
	for i, c := range cams {
		if c.String("id") == id {
			return i, true
		}
	}
	return 0, false
}

// camerasPatch builds the smallest document that replaces the camera list.
//
// Masked passwords travel back untouched: the agent restores the stored
// password for any camera whose id it already knows, so editing one camera
// never requires the hub to hold the others' credentials.
func camerasPatch(cams []Doc) ([]byte, error) {
	list := make([]any, 0, len(cams))
	for _, c := range cams {
		list = append(list, map[string]any(c))
	}
	return json.Marshal(map[string]any{"cameras": list})
}

// CertPair is a certificate and key read from disk, with what the certificate
// says about itself.
type CertPair struct {
	CertPEM string
	KeyPEM  string
	Info    certs.Info
	leaf    *x509.Certificate
}

// LoadCertPair reads and checks a certificate pair before a single byte goes to
// the fleet. Pushing a broken pair to fifty servers would take every nursery
// page offline at once, so the key has to match the certificate here, locally.
func LoadCertPair(certFile, keyFile string) (*CertPair, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("сертификат: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("приватный ключ: %w", err)
	}

	pair, err := certs.LoadPair(string(certPEM), string(keyPEM))
	if err != nil {
		return nil, err
	}
	info := certs.Describe(pair)
	if info.DaysLeft < 0 {
		return nil, fmt.Errorf("сертификат истёк %s", info.NotAfter.Format("2006-01-02"))
	}
	return &CertPair{
		CertPEM: string(certPEM),
		KeyPEM:  string(keyPEM),
		Info:    info,
		leaf:    pair.Leaf,
	}, nil
}

// Covers reports whether the certificate is valid for a site's domain. A
// wildcard certificate for one fleet is the normal case, and this is what stops
// the operator from pushing the wrong one to every site.
func (p *CertPair) Covers(domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return errors.New("у площадки не задан домен, проверить покрытие нечем")
	}
	if err := p.leaf.VerifyHostname(domain); err != nil {
		return fmt.Errorf("сертификат не выписан на %s (в нём %s)", domain, strings.Join(p.Info.Hosts, ", "))
	}
	return nil
}

// Patch builds the settings document that installs the pair.
func (p *CertPair) Patch() ([]byte, error) {
	return json.Marshal(map[string]any{
		"tls": map[string]any{
			"cert_pem": p.CertPEM,
			"key_pem":  p.KeyPEM,
		},
	})
}

// ReadPatchFile loads a settings document from disk and checks that it is a
// JSON object, so a mistyped file is rejected before it reaches any agent.
func ReadPatchFile(path string) ([]byte, Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	// Notepad and Out-File leave a BOM; the agent tolerates one in its own
	// config, and there is no reason for the hub to be stricter.
	raw = trimBOM(raw)

	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: нужен объект JSON с настройками: %w", path, err)
	}
	return raw, doc, nil
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return b[3:]
	}
	return b
}

// clone returns a deep copy of the document. Settings travel from the inventory
// into a request body and back, and a shared map would let one site's edit show
// up in another's.
func (d Doc) clone() Doc {
	if d == nil {
		return nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	var cp Doc
	if json.Unmarshal(raw, &cp) != nil {
		return nil
	}
	return cp
}

// validate checks the parts of a settings document the hub understands, so a
// typo in the inventory is caught before it is sent to forty-five servers. The
// rest of the document is deliberately not policed: the agent validates it, and
// this build must not reject a field a newer agent introduced.
func (d Doc) validate() error {
	if d == nil {
		return nil
	}

	cams, err := d.Cameras()
	if err != nil {
		return err
	}

	var probs []string
	seen := map[string]bool{}
	for i, cam := range cams {
		where := fmt.Sprintf("cameras[%d]", i)

		id := cam.String("id")
		if !camIDRe.MatchString(id) {
			probs = append(probs, fmt.Sprintf("%s: недопустимый id %q", where, id))
		}
		if seen[id] {
			probs = append(probs, where+": id дублируется: "+id)
		}
		seen[id] = true

		if strings.TrimSpace(cam.String("name")) == "" {
			probs = append(probs, where+": name пуст")
		}
		// A masked URL is what pull records for a site whose password the
		// inventory does not hold; it is valid and means "keep what the agent
		// has", so only a present, non-masked URL is checked.
		if u := cam.String("url"); !strings.Contains(u, Mask) {
			if err := validateRTSP(u); err != nil {
				probs = append(probs, where+": "+err.Error())
			}
		}
	}

	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

// unmaskFrom fills every masked value in d with the real one from kept.
//
// The agent answers GET /api/settings with secrets replaced by Mask, so a pull
// alone could never record a camera password. Whatever the inventory already
// holds therefore wins over a mask — otherwise one pull would quietly erase the
// passwords the operator typed in, and the file would stop being able to
// rebuild a site from scratch.
func (d Doc) unmaskFrom(kept Doc) {
	if d == nil || kept == nil {
		return
	}

	if in, ok := d["tls"].(map[string]any); ok {
		if old, ok := kept["tls"].(map[string]any); ok {
			for _, key := range []string{"key_pem", "cert_pem"} {
				if s, _ := in[key].(string); s == Mask {
					if prev, exists := old[key]; exists {
						in[key] = prev
					}
				}
			}
		}
	}

	cams, err := d.Cameras()
	if err != nil {
		return
	}
	oldCams, err := kept.Cameras()
	if err != nil {
		return
	}
	for _, cam := range cams {
		i, ok := findCamera(oldCams, cam.String("id"))
		if !ok {
			continue
		}
		if url := cam.String("url"); strings.Contains(url, Mask) {
			if prev := oldCams[i].String("url"); prev != "" && !strings.Contains(prev, Mask) {
				cam["url"] = prev
			}
		}
	}
}

// DiffFields lists the top-level settings keys where want and got disagree.
//
// Masked values in either document are skipped: a mask carries no information,
// so comparing it to a secret would report a difference on every run and make
// the command useless.
func DiffFields(want, got Doc) []string {
	if want == nil {
		return nil
	}

	var out []string
	for key, wantVal := range want {
		gotVal, present := got[key]
		if !present {
			out = append(out, key)
			continue
		}
		if !equalIgnoringMask(wantVal, gotVal) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// equalIgnoringMask compares two values, treating a mask on either side as
// "matches whatever is opposite".
func equalIgnoringMask(a, b any) bool {
	if s, ok := a.(string); ok && strings.Contains(s, Mask) {
		return true
	}
	if s, ok := b.(string); ok && strings.Contains(s, Mask) {
		return true
	}

	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			other, exists := bv[k]
			if !exists || !equalIgnoringMask(v, other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalIgnoringMask(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// mergeInto folds a settings patch into a recorded document the way the agent
// folds it into its own configuration: a key that is an object on both sides is
// merged field by field, anything else is replaced, and an array replaces the
// array it lands on.
//
// Mirroring the agent matters. If the hub recorded {"hls":{"low_latency":true}}
// as the whole hls block while the agent merely updated one field of its own,
// the inventory would claim settings the site does not have and the next push
// would quietly undo the rest.
func mergeInto(dst, src Doc) {
	for key, val := range src {
		sub, isObj := val.(map[string]any)
		if !isObj {
			dst[key] = val
			continue
		}
		cur, ok := dst[key].(map[string]any)
		if !ok {
			dst[key] = Doc(sub).clone()
			continue
		}
		mergeInto(Doc(cur), Doc(sub))
	}
}

// unmanaged are the settings the inventory deliberately does not own.
//
// control holds the site's own token and the address the hub reaches it on —
// bootstrap, not desired state. tls holds the certificate, whose private key
// the agent never hands out, so a recorded copy could restore nothing. web_dir
// is a path on the site's own disk, and the fleet is mixed: ProgramData on
// Windows, /var/lib on Linux. One site's path pushed to another would move the
// page to a directory belonging to a different operating system — and the agent
// would obligingly create it and serve an empty page from there.
//
// All of them are stripped wherever settings enter or leave the inventory. A
// stale entry — left by an older build, or typed in by hand — would otherwise
// be sent by push and could move the control listener, switch its TLS off or
// relocate the page, cutting the hub off from the site it was configuring.
var unmanaged = []string{"control", "tls", "web_dir"}

// stripUnmanaged removes those blocks and reports whether anything went.
func stripUnmanaged(d Doc) bool {
	var removed bool
	for _, key := range unmanaged {
		if _, present := d[key]; present {
			delete(d, key)
			removed = true
		}
	}
	return removed
}
