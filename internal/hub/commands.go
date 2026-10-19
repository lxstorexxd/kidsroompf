package hub

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// List prints the inventory.
func List(o *Options) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}
	return RenderInventory(o.Out, inv, o.JSON)
}

// AddParams is what `add` needs beyond the inventory.
type AddParams struct {
	Name  string
	Addr  string
	Token string
	Tags  []string
	Note  string
	Pin   bool
}

// Add registers a site after checking that it actually answers.
//
// The check is the point: a mistyped address or token found at add time costs a
// second, and found later costs a failed fleet-wide change that has to be
// unpicked site by site.
func Add(o *Options, p AddParams) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}

	site := Site{
		Name:  strings.ToLower(strings.TrimSpace(p.Name)),
		Addr:  strings.TrimRight(strings.TrimSpace(p.Addr), "/"),
		Token: strings.TrimSpace(p.Token),
		Tags:  normaliseTags(p.Tags),
		Note:  strings.TrimSpace(p.Note),
	}
	if err := site.Validate(); err != nil {
		return err
	}
	if _, exists := inv.Find(site.Name); exists {
		return fmt.Errorf("площадка %q уже есть в инвентаре", site.Name)
	}

	c := o.client(site)
	st, err := c.Status()
	if err != nil {
		return fmt.Errorf("площадка не отвечает, в инвентарь не добавлена: %w", err)
	}
	if p.Pin {
		if fp := c.Fingerprint(); fp != "" {
			site.Fingerprint = fp
		} else {
			fmt.Fprintln(o.Out, "закреплять отпечаток нечего: площадка отвечает по http, без TLS")
		}
	}

	// Record what the site has straight away: the inventory is the source of
	// truth, and a site registered but never pulled would be a hole in it.
	if doc, err := c.Settings(); err != nil {
		fmt.Fprintf(o.Out, "настройки площадки записать не удалось (%v) — выполните pull\n", err)
	} else {
		stripUnmanaged(doc)
		site.Settings = doc
	}

	if err := inv.Add(site); err != nil {
		return err
	}
	if err := inv.Save(); err != nil {
		return err
	}

	ready, enabled := st.CamerasReady()
	fmt.Fprintf(o.Out, "добавлена площадка %s: агент %s, домен %s, камеры %d/%d\n",
		site.Name, firstNonEmpty(st.Version, "неизвестной версии"),
		firstNonEmpty(st.Domain, "не задан"), ready, enabled)
	if site.Fingerprint != "" {
		fmt.Fprintf(o.Out, "отпечаток сертификата закреплён: %s\n", site.Fingerprint)
	}
	return nil
}

func normaliseTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		for _, part := range strings.Split(t, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// Remove drops a site from the inventory. The agent itself is untouched: it
// keeps streaming, it is simply no longer managed from here.
func Remove(o *Options, name string) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}
	if err := inv.Remove(name); err != nil {
		return err
	}
	if err := inv.Save(); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "площадка %s убрана из инвентаря (агент на ней продолжает работать)\n", name)
	return nil
}

// ShowStatus collects the state of every selected site.
func ShowStatus(o *Options, sel Selection) (int, error) {
	sites, err := o.sites(sel)
	if err != nil {
		return 0, err
	}

	results := Run(sites, o.Parallel, false, func(s Site) (any, error) {
		return o.client(s).Status()
	})
	if err := RenderStatus(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// Get writes one site's settings, with secrets masked by the agent. The output
// is meant to be edited and sent back with apply: values left as *** keep what
// is stored, so the file never has to hold a password.
func Get(o *Options, name, outFile string) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}
	site, ok := inv.Find(name)
	if !ok {
		return fmt.Errorf("площадка %q не найдена в инвентаре", name)
	}

	raw, err := o.client(site).SettingsRaw()
	if err != nil {
		return err
	}
	if outFile == "" {
		_, err = o.Out.Write(raw)
		return err
	}
	if err := os.WriteFile(outFile, raw, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "настройки площадки %s записаны в %s\n", site.Name, outFile)
	return nil
}

// Apply sends a settings document to the selection. The document may be partial:
// the agent keeps every key it does not mention.
func Apply(o *Options, sel Selection, file string) (int, error) {
	body, doc, err := ReadPatchFile(file)
	if err != nil {
		return 0, err
	}
	if len(doc) == 0 {
		return 0, fmt.Errorf("%s не содержит ни одного поля", file)
	}

	what := fmt.Sprintf("Применить %s (поля: %s)", file, strings.Join(keys(doc), ", "))
	results, err := o.recordAndSend(sel, what, func(s Site) (change, error) {
		// The patch is folded into the recorded settings the same way the agent
		// folds it into its own configuration, so the file keeps describing what
		// the site actually has rather than only the fields the patch mentioned.
		next, err := o.baseSettings(s)
		if err != nil {
			return change{}, err
		}
		mergeInto(next, doc)
		return change{next: next, body: body, msg: "настройки применены"}, nil
	})
	if err != nil {
		return 0, err
	}
	return Failures(results), nil
}

func keys(d Doc) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	return out
}

// Restart restarts the stream on the selection. The page and the API stay up;
// only mediamtx is cycled, which costs viewers a second or two of video.
func Restart(o *Options, sel Selection) (int, error) {
	sites, err := o.sites(sel)
	if err != nil {
		return 0, err
	}
	if err := o.confirm(sites, "Перезапустить поток"); err != nil {
		return 0, err
	}

	results := Run(sites, o.writeParallel(), o.StopOnError, func(s Site) (any, error) {
		return "поток перезапущен", o.client(s).Restart()
	})
	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// sites resolves a selection against the inventory.
func (o *Options) sites(sel Selection) ([]Site, error) {
	inv, err := o.inventory()
	if err != nil {
		return nil, err
	}
	return inv.Resolve(sel)
}

// TokenShow prints a site's token in full. It is the one command that does so,
// because that is the value the operator has to carry to the server.
func TokenShow(o *Options, name string) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}
	site, ok := inv.Find(name)
	if !ok {
		return fmt.Errorf("площадка %q не найдена в инвентаре", name)
	}
	fmt.Fprintln(o.Out, site.Token)
	return nil
}

// TokenRotate asks the agent for a new token and stores it.
//
// The order matters and cannot be made safe by retrying: the agent switches
// tokens the moment it answers, so if the inventory write failed the site would
// be unreachable. The new token is therefore printed before anything else, so
// it can be recovered by hand from the scrollback.
func TokenRotate(o *Options, name string) error {
	inv, err := o.inventory()
	if err != nil {
		return err
	}
	site, ok := inv.Find(name)
	if !ok {
		return fmt.Errorf("площадка %q не найдена в инвентаре", name)
	}

	token, err := o.client(site).RotateToken()
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "новый токен площадки %s: %s\n", site.Name, token)

	site.Token = token
	if err := inv.Update(site); err != nil {
		return err
	}
	if err := inv.Save(); err != nil {
		return fmt.Errorf("токен на площадке уже заменён, но инвентарь не записан (%w) — впишите токен выше в %s вручную",
			err, inv.Path())
	}
	fmt.Fprintf(o.Out, "инвентарь обновлён: %s\n", inv.Path())
	return nil
}

// Pin records the certificate a site currently presents, so a later change of
// certificate becomes visible instead of silent.
func Pin(o *Options, sel Selection, clear bool) (int, error) {
	inv, err := o.inventory()
	if err != nil {
		return 0, err
	}
	sites, err := inv.Resolve(sel)
	if err != nil {
		return 0, err
	}

	var changed int
	results := Run(sites, o.Parallel, false, func(s Site) (any, error) {
		if clear {
			return "отпечаток снят", nil
		}
		c := o.client(s)
		if _, err := c.Status(); err != nil {
			return nil, err
		}
		fp := c.Fingerprint()
		if fp == "" {
			return nil, errors.New("площадка отвечает по http, закреплять нечего")
		}
		return fp, nil
	})

	for i, r := range results {
		if r.Err != nil {
			continue
		}
		site := sites[i]
		if clear {
			site.Fingerprint = ""
		} else {
			fp, _ := r.Data.(string)
			site.Fingerprint = fp
			results[i].Data = "отпечаток закреплён: " + fp
		}
		if err := inv.Update(site); err != nil {
			results[i].Err = err
			continue
		}
		changed++
	}

	if changed > 0 {
		if err := inv.Save(); err != nil {
			return 0, err
		}
	}
	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// Check validates the inventory without touching the network.
//
// A file edited by hand for forty-five sites is where typos live, and a typo in
// a camera id or an RTSP address would otherwise surface one site at a time in
// the middle of a rollout. This reads the file and says everything that is
// wrong with it at once.
func Check(o *Options) (int, error) {
	inv, err := o.inventory()
	if err != nil {
		return 0, err
	}
	if len(inv.Sites) == 0 {
		fmt.Fprintf(o.Out, "инвентарь пуст: %s\n", inv.Path())
		return 0, nil
	}

	var problems, noToken, noSettings int
	for _, s := range inv.Sites {
		if err := s.Validate(); err != nil {
			fmt.Fprintf(o.Out, "%s  ОШИБКА: %v\n", s.Name, err)
			problems++
		}
		if strings.TrimSpace(s.Token) == "" {
			noToken++
		}
		if len(s.Settings) == 0 {
			noSettings++
		}
	}

	enabled, total := 0, 0
	for _, s := range inv.Sites {
		e, t := s.CameraCount()
		enabled += e
		total += t
	}

	fmt.Fprintf(o.Out, "\nплощадок: %d, камер: %d (включено %d)\n", len(inv.Sites), total, enabled)
	if noToken > 0 {
		fmt.Fprintf(o.Out, "без токена: %d — возьмите его на сервере (kidsroom-agent.exe token) и впишите в %s\n",
			noToken, inv.Path())
	}
	if noSettings > 0 {
		fmt.Fprintf(o.Out, "без записанных настроек: %d — заберите их с площадок командой pull\n", noSettings)
	}
	if problems == 0 {
		fmt.Fprintln(o.Out, "ошибок в файле нет")
		return 0, nil
	}
	fmt.Fprintf(o.Out, "площадок с ошибками: %d\n", problems)
	return problems, nil
}
