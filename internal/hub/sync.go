package hub

import (
	"errors"
	"fmt"
	"strings"
)

// Push sends each site the settings recorded for it in the inventory.
//
// This is what makes the inventory the source of truth: a site that was
// reinstalled, or one whose settings somebody changed on the spot, is brought
// back to what the file says. A site with nothing recorded is skipped rather
// than wiped — the file not knowing about a site is not a statement that the
// site should have no cameras.
func Push(o *Options, sel Selection) (int, error) {
	sites, err := o.sites(sel)
	if err != nil {
		return 0, err
	}

	var ready []Site
	for _, s := range sites {
		if len(s.Settings) > 0 {
			ready = append(ready, s)
		}
	}
	if len(ready) == 0 {
		return 0, errors.New("ни у одной из выбранных площадок нет записанных настроек: сначала pull (забрать с площадок) или впишите их в инвентарь")
	}
	if skipped := len(sites) - len(ready); skipped > 0 {
		fmt.Fprintf(o.Out, "без записанных настроек, пропускаю: %d\n", skipped)
	}

	if err := o.confirm(ready, "Отправить записанные настройки"); err != nil {
		return 0, err
	}

	results := Run(ready, o.writeParallel(), o.StopOnError, func(s Site) (any, error) {
		send := s.Settings.clone()
		if stripUnmanaged(send) {
			fmt.Fprintf(o.Err, "%s: в записанных настройках были control/tls/web_dir — не отправляю их (перезапишите запись командой pull)\n", s.Name)
		}
		body, err := send.Marshal()
		if err != nil {
			return nil, err
		}
		if o.DryRun {
			return "--dry-run, было бы отправлено: " + string(body), nil
		}
		if err := o.client(s).Apply(body); err != nil {
			return nil, err
		}
		enabled, total := s.CameraCount()
		return fmt.Sprintf("настройки отправлены (%s, камер %d/%d)",
			firstNonEmpty(s.Settings.String("domain"), "домен не задан"), enabled, total), nil
	})

	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// Pull records what each site currently has into the inventory.
//
// Secrets come back masked, so what the inventory already holds wins over a
// mask: one pull must not erase the camera passwords that let the file rebuild
// a site from nothing.
func Pull(o *Options, sel Selection) (int, error) {
	inv, err := o.inventory()
	if err != nil {
		return 0, err
	}
	sites, err := inv.Resolve(sel)
	if err != nil {
		return 0, err
	}

	results := Run(sites, o.Parallel, o.StopOnError, func(s Site) (any, error) {
		doc, err := o.client(s).Settings()
		if err != nil {
			return nil, err
		}
		stripUnmanaged(doc)

		doc.unmaskFrom(s.Settings)
		return doc, nil
	})

	var changed int
	for i := range results {
		if results[i].Err != nil {
			continue
		}
		doc, ok := results[i].Data.(Doc)
		if !ok {
			continue
		}

		site := sites[i]
		was := DiffFields(doc, site.Settings)
		site.Settings = doc
		if err := inv.Update(site); err != nil {
			results[i].Err = err
			continue
		}
		changed++

		enabled, total := site.CameraCount()
		msg := fmt.Sprintf("записано: %s, камер %d/%d",
			firstNonEmpty(doc.String("domain"), "домен не задан"), enabled, total)
		if len(was) > 0 {
			msg += " (изменились: " + strings.Join(was, ", ") + ")"
		}
		results[i].Data = msg
	}

	if changed > 0 {
		if o.DryRun {
			fmt.Fprintln(o.Out, "--dry-run: инвентарь не записан")
		} else if err := inv.Save(); err != nil {
			return 0, fmt.Errorf("настройки получены, но инвентарь не записан: %w", err)
		}
	}

	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}

// Diff reports where a site disagrees with the inventory.
//
// It answers the question the single-file model creates: the file says one
// thing, the site may say another. Masked values are ignored — a mask carries
// no information, and comparing it to a secret would report a difference every
// time and make the command worthless.
func Diff(o *Options, sel Selection) (int, error) {
	sites, err := o.sites(sel)
	if err != nil {
		return 0, err
	}

	results := Run(sites, o.Parallel, false, func(s Site) (any, error) {
		live, err := o.client(s).Settings()
		if err != nil {
			return nil, err
		}
		stripUnmanaged(live)

		if len(s.Settings) == 0 {
			return "в инвентаре настроек нет (забрать: pull)", nil
		}
		// A stale entry may still carry those blocks; comparing them would
		// report a difference the inventory does not claim to own.
		want := s.Settings.clone()
		stripUnmanaged(want)

		fields := DiffFields(want, live)
		if len(fields) == 0 {
			return "совпадает", nil
		}
		return "расходится: " + strings.Join(fields, ", "), nil
	})

	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return 0, err
	}
	return Failures(results), nil
}
