package hub

import (
	"fmt"
)

// change is one site's part of a command: the settings to record for it and the
// smallest request body that applies the change.
type change struct {
	next Doc    // settings to store in the inventory
	body []byte // what to send to the agent
	msg  string // what to print for this site
}

// recordAndSend applies a change across the selection and keeps the inventory
// in step with it.
//
// Both halves matter. Sending without recording would leave the file claiming
// settings the fleet no longer has; recording without sending would leave the
// fleet behind the file. Sites that refused keep their old entry, so a failed
// half of a fleet-wide change shows up in diff instead of silently becoming the
// recorded truth.
func (o *Options) recordAndSend(sel Selection, what string, build func(Site) (change, error)) ([]Result, error) {
	inv, err := o.inventory()
	if err != nil {
		return nil, err
	}
	sites, err := inv.Resolve(sel)
	if err != nil {
		return nil, err
	}
	if err := o.confirm(sites, what); err != nil {
		return nil, err
	}

	results := Run(sites, o.writeParallel(), o.StopOnError, func(s Site) (any, error) {
		ch, err := build(s)
		if err != nil {
			return nil, err
		}
		if o.DryRun {
			return change{msg: "--dry-run, было бы отправлено: " + string(ch.body)}, nil
		}
		if err := o.client(s).Apply(ch.body); err != nil {
			return nil, err
		}
		return ch, nil
	})

	var changed int
	for i := range results {
		if results[i].Err != nil {
			continue
		}
		ch, ok := results[i].Data.(change)
		if !ok {
			continue
		}
		results[i].Data = ch.msg

		if ch.next == nil || o.DryRun {
			continue
		}
		site := sites[i]
		site.Settings = ch.next
		if err := inv.Update(site); err != nil {
			results[i].Err = err
			continue
		}
		changed++
	}

	if changed > 0 {
		if err := inv.Save(); err != nil {
			return nil, fmt.Errorf("площадки изменены, но инвентарь не записан: %w — выполните pull", err)
		}
	}

	if err := RenderActions(o.Out, results, o.JSON); err != nil {
		return nil, err
	}
	return results, nil
}

// baseSettings is the document a change should be built on.
//
// The inventory is the source of truth, so its entry is used when it has one.
// When it does not — a site added but never pulled — the agent is asked, and
// what comes back is folded over whatever the inventory holds so that masked
// secrets do not travel into the file.
func (o *Options) baseSettings(s Site) (Doc, error) {
	if len(s.Settings) > 0 {
		base := s.Settings.clone()
		stripUnmanaged(base)
		return base, nil
	}

	doc, err := o.client(s).Settings()
	if err != nil {
		return nil, fmt.Errorf("в инвентаре настроек нет, и площадка их не отдала: %w", err)
	}
	stripUnmanaged(doc)
	doc.unmaskFrom(s.Settings)
	return doc, nil
}
