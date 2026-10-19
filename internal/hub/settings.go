package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var camIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// SetDomain writes the public domain.
//
// On a site still running the self-signed certificate the agent reissues it for
// the new name, which changes the certificate — so a pinned fingerprint is
// dropped rather than left pointing at a certificate that no longer exists.
func SetDomain(o *Options, sel Selection, domain string) (int, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return 0, errors.New("домен пуст")
	}
	if strings.ContainsAny(domain, ` /\:`) {
		return 0, errors.New("домен должен быть именем хоста без схемы и порта")
	}
	// A non-ASCII name cannot go into a certificate (SAN entries are IA5String),
	// so every site would answer with the same refusal. Saying it once here is
	// better than collecting fifty identical 400-х.
	for _, r := range domain {
		if r > unicode.MaxASCII {
			return 0, fmt.Errorf("домен %q содержит не-ASCII символы: укажите его в punycode (xn--…), иначе сертификат под него не выписать", domain)
		}
	}

	body, err := json.Marshal(map[string]any{"domain": domain})
	if err != nil {
		return 0, err
	}

	results, err := o.recordAndSend(sel, "Сменить домен на "+domain, func(s Site) (change, error) {
		next := s.Settings.clone()
		if next == nil {
			next = Doc{}
		}
		next["domain"] = domain
		return change{next: next, body: body, msg: "домен: " + domain}, nil
	})
	if err != nil {
		return 0, err
	}
	// A site still on its self-signed certificate reissues it for the new name,
	// so a pinned fingerprint would point at a certificate that no longer exists.
	o.unpinChanged(results)
	return Failures(results), nil
}

// Fingerprint is the SHA-256 of the certificate, the same value the hub pins.
func (p *CertPair) Fingerprint() string {
	sum := sha256.Sum256(p.leaf.Raw)
	return hex.EncodeToString(sum[:])
}

// SetCert installs one certificate pair across the selection.
//
// Every site is checked against the certificate's names first. A wildcard for
// the fleet is the normal case, and this is what keeps the wrong file from
// taking fifty nursery pages down at once; --force is there for the operator
// who knows better.
func SetCert(o *Options, sel Selection, certFile, keyFile string, force bool) (int, error) {
	pair, err := LoadCertPair(certFile, keyFile)
	if err != nil {
		return 0, err
	}
	body, err := pair.Patch()
	if err != nil {
		return 0, err
	}

	sites, err := o.sites(sel)
	if err != nil {
		return 0, err
	}

	fmt.Fprintf(o.Out, "сертификат: %s, выдан %s, действует до %s (%d дн.), имена: %s\n",
		pair.Info.Subject, pair.Info.Issuer, pair.Info.NotAfter.Format("2006-01-02"),
		pair.Info.DaysLeft, strings.Join(pair.Info.Hosts, ", "))

	if o.DryRun {
		// The body is the certificate itself; printing it would bury the useful
		// part, so only the plan is shown.
		names := make([]string, 0, len(sites))
		for _, s := range sites {
			names = append(names, s.Name)
		}
		fmt.Fprintf(o.Out, "--dry-run: ничего не отправлено\nплощадки (%d): %s\n", len(sites), strings.Join(names, ", "))
		return 0, nil
	}
	if err := o.confirm(sites, "Установить сертификат "+certFile); err != nil {
		return 0, err
	}

	results := Run(sites, o.writeParallel(), o.StopOnError, func(s Site) (any, error) {
		c := o.client(s)
		if !force {
			st, err := c.Status()
			if err != nil {
				return nil, err
			}
			if err := pair.Covers(st.Domain); err != nil {
				return nil, fmt.Errorf("%w (отправить всё равно: --force)", err)
			}
		}
		if err := c.Apply(body); err != nil {
			return nil, err
		}
		return fmt.Sprintf("сертификат установлен, действует до %s", pair.Info.NotAfter.Format("2006-01-02")), nil
	})
	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}

	// The hub installed this certificate, so it knows the fingerprint the site
	// will present from now on and can move the pin itself.
	o.repinTo(results, pair.Fingerprint())
	return Failures(results), nil
}

// CameraSpec is a camera as named on the command line. A nil Enabled means the
// flag was not given, which for `set` is the difference between "switch it off"
// and "leave it as it is".
type CameraSpec struct {
	ID      string
	Name    string
	URL     string
	Enabled *bool
}

func (spec CameraSpec) validateNew() error {
	var probs []string
	if !camIDRe.MatchString(spec.ID) {
		probs = append(probs, fmt.Sprintf("id %q: нужны строчные латинские буквы, цифры, _ и -", spec.ID))
	}
	if strings.TrimSpace(spec.Name) == "" {
		probs = append(probs, "имя камеры пусто")
	}
	if err := validateRTSP(spec.URL); err != nil {
		probs = append(probs, err.Error())
	}
	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

func validateRTSP(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url камеры пуст")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("url камеры не разобрать: " + err.Error())
	}
	if u.Scheme != "rtsp" && u.Scheme != "rtsps" {
		return errors.New("url камеры должен начинаться с rtsp:// или rtsps://")
	}
	if u.Host == "" {
		return errors.New("в url камеры нет хоста")
	}
	return nil
}

// CameraAdd adds one camera to every selected site.
func CameraAdd(o *Options, sel Selection, spec CameraSpec) (int, error) {
	if err := spec.validateNew(); err != nil {
		return 0, err
	}
	enabled := true
	if spec.Enabled != nil {
		enabled = *spec.Enabled
	}

	return o.editCameras(sel, fmt.Sprintf("Добавить камеру %s (%s)", spec.ID, spec.Name),
		func(cams []Doc) ([]Doc, string, error) {
			if _, ok := findCamera(cams, spec.ID); ok {
				return nil, "", fmt.Errorf("камера %s на этой площадке уже есть", spec.ID)
			}
			cams = append(cams, Doc{
				"id":      spec.ID,
				"name":    spec.Name,
				"url":     spec.URL,
				"enabled": enabled,
			})
			return cams, "камера " + spec.ID + " добавлена", nil
		})
}

// CameraSet changes the fields that were given and leaves the rest alone.
func CameraSet(o *Options, sel Selection, id string, spec CameraSpec) (int, error) {
	if spec.Name == "" && spec.URL == "" && spec.Enabled == nil {
		return 0, errors.New("нечего менять: укажите --name, --url, --enable или --disable")
	}
	if spec.URL != "" {
		if err := validateRTSP(spec.URL); err != nil {
			return 0, err
		}
	}

	return o.editCameras(sel, "Изменить камеру "+id,
		func(cams []Doc) ([]Doc, string, error) {
			i, ok := findCamera(cams, id)
			if !ok {
				return nil, "", fmt.Errorf("камера %s на этой площадке не найдена", id)
			}
			var changed []string
			if spec.Name != "" {
				cams[i]["name"] = spec.Name
				changed = append(changed, "имя")
			}
			if spec.URL != "" {
				cams[i]["url"] = spec.URL
				changed = append(changed, "url")
			}
			if spec.Enabled != nil {
				cams[i]["enabled"] = *spec.Enabled
				if *spec.Enabled {
					changed = append(changed, "включена")
				} else {
					changed = append(changed, "выключена")
				}
			}
			return cams, fmt.Sprintf("камера %s: %s", id, strings.Join(changed, ", ")), nil
		})
}

// CameraRemove deletes a camera.
func CameraRemove(o *Options, sel Selection, id string) (int, error) {
	return o.editCameras(sel, "Удалить камеру "+id,
		func(cams []Doc) ([]Doc, string, error) {
			i, ok := findCamera(cams, id)
			if !ok {
				return nil, "", fmt.Errorf("камера %s на этой площадке не найдена", id)
			}
			return append(cams[:i], cams[i+1:]...), "камера " + id + " удалена", nil
		})
}

// editCameras reads each site's camera list, lets edit rewrite it and sends
// back only the list.
//
// Reading first is unavoidable — the list is per-site — and it is also what
// makes the operation safe: the URLs come back from the agent masked, and
// sending a mask tells the agent to keep the password it already has. The hub
// never handles the other cameras' credentials.
func (o *Options) editCameras(sel Selection, what string, edit func([]Doc) ([]Doc, string, error)) (int, error) {
	results, err := o.recordAndSend(sel, what, func(s Site) (change, error) {
		base, err := o.baseSettings(s)
		if err != nil {
			return change{}, err
		}
		cams, err := base.Cameras()
		if err != nil {
			return change{}, err
		}

		edited, msg, err := edit(cams)
		if err != nil {
			return change{}, err
		}
		body, err := camerasPatch(edited)
		if err != nil {
			return change{}, err
		}

		list := make([]any, 0, len(edited))
		for _, c := range edited {
			list = append(list, map[string]any(c))
		}
		base["cameras"] = list

		return change{next: base, body: body, msg: msg}, nil
	})
	if err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// SetControlTLS turns the control API's own TLS on or off and follows the
// change in the inventory.
//
// The agent rebinds the listener as part of applying the setting, which drops
// the very connection carrying the answer — so a transport error here means
// nothing in itself. The change is confirmed by reaching the site again on the
// new scheme, and the inventory address is rewritten only for the sites that
// answered. Without that last step, switching off TLS would leave the whole
// fleet unreachable until the file was edited by hand.
func SetControlTLS(o *Options, sel Selection, on bool) (int, error) {
	inv, err := o.inventory()
	if err != nil {
		return 0, err
	}
	sites, err := inv.Resolve(sel)
	if err != nil {
		return 0, err
	}

	body, err := json.Marshal(map[string]any{"control": map[string]any{"tls": on}})
	if err != nil {
		return 0, err
	}
	scheme := "http"
	state := "выключен"
	if on {
		scheme, state = "https", "включён"
	}

	if o.dryRun(sites, body) {
		return 0, nil
	}
	if err := o.confirm(sites, "TLS на control API будет "+state+", адрес в инвентаре станет "+scheme+"://"); err != nil {
		return 0, err
	}

	// Sequential: this reconfigures the channel the hub itself is using, and a
	// half-finished parallel run would be hard to reason about.
	results := Run(sites, 1, false, func(s Site) (any, error) {
		applyErr := o.client(s).Apply(body)

		moved := s
		moved.Addr = scheme + "://" + hostPart(s.Addr)
		// The pin belonged to the old channel, and over plain http there is no
		// certificate to pin at all.
		moved.Fingerprint = ""

		// Reaching the site on the new scheme is not proof of anything: someone
		// may have been answering there all along. The setting is read back and
		// compared, so a request that never arrived is reported as a failure
		// instead of being written into the inventory as a success.
		doc, err := o.client(moved).Settings()
		if err != nil {
			if applyErr != nil {
				return nil, fmt.Errorf("настройка не применилась: %w", applyErr)
			}
			return nil, fmt.Errorf("настройка отправлена, но площадка не отвечает по %s: %w", scheme, err)
		}
		if got := doc.Section("control")["tls"]; got != on {
			if applyErr != nil {
				return nil, fmt.Errorf("настройка не применилась: %w", applyErr)
			}
			return nil, fmt.Errorf("площадка сообщает control.tls=%v, ожидалось %v", got, on)
		}
		return moved, nil
	})

	var changed int
	for i := range results {
		if results[i].Err != nil {
			continue
		}
		moved, ok := results[i].Data.(Site)
		if !ok {
			continue
		}
		if err := inv.Update(moved); err != nil {
			results[i].Err = err
			continue
		}
		changed++
		results[i].Data = "TLS " + state + ", адрес в инвентаре: " + moved.Addr
	}
	if changed > 0 {
		if err := inv.Save(); err != nil {
			return 0, fmt.Errorf("площадки переключены, но инвентарь не записан: %w", err)
		}
	}

	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// hostPart strips the scheme, leaving host:port.
func hostPart(addr string) string {
	if i := strings.Index(addr, "://"); i >= 0 {
		return strings.TrimRight(addr[i+3:], "/")
	}
	return strings.TrimRight(addr, "/")
}

// unpinChanged drops the stored fingerprint for sites whose certificate the
// change may have replaced.
func (o *Options) unpinChanged(results []Result) {
	o.repinTo(results, "")
}

// repinTo moves the stored fingerprint of every site that succeeded. An empty
// value clears it, which is the honest answer when the hub cannot know what the
// site will present next.
func (o *Options) repinTo(results []Result, fingerprint string) {
	inv, err := o.inventory()
	if err != nil {
		return
	}

	var changed bool
	for _, r := range results {
		if r.Err != nil || r.Site.Fingerprint == "" {
			continue
		}
		site, ok := inv.Find(r.Site.Name)
		if !ok {
			continue
		}
		if site.Fingerprint == fingerprint {
			continue
		}
		site.Fingerprint = fingerprint
		if inv.Update(site) == nil {
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := inv.Save(); err != nil {
		fmt.Fprintf(o.Err, "не удалось обновить отпечатки в инвентаре: %v\n", err)
		return
	}
	if fingerprint == "" {
		fmt.Fprintln(o.Out, "закреплённые отпечатки сняты: сертификат площадок мог измениться (закрепить заново: pin)")
	} else {
		fmt.Fprintln(o.Out, "закреплённые отпечатки обновлены под новый сертификат")
	}
}
