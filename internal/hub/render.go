package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// table lays out rows in aligned columns. The last column is never padded, so a
// long warning can run to the end of the line without pushing the table apart.
type table struct {
	head []string
	rows [][]string
}

func newTable(head ...string) *table { return &table{head: head} }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) write(w io.Writer) {
	widths := make([]int, len(t.head))
	for i, h := range t.head {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, c := range row {
			if i < len(widths) && utf8.RuneCountInString(c) > widths[i] {
				widths[i] = utf8.RuneCountInString(c)
			}
		}
	}

	writeRow(w, t.head, widths)
	for _, row := range t.rows {
		writeRow(w, row, widths)
	}
}

func writeRow(w io.Writer, cells []string, widths []int) {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(c)
		if i < len(cells)-1 && i < len(widths) {
			b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)))
		}
	}
	fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

// RenderStatus prints the fleet overview.
func RenderStatus(w io.Writer, results []Result, asJSON bool) error {
	if asJSON {
		return writeJSON(w, results, func(r Result) any {
			// The agent's own answer goes through untouched, so a field the hub
			// does not model is not quietly dropped on the way to a script.
			if st, ok := r.Data.(*Status); ok && len(st.Raw()) > 0 {
				return json.RawMessage(st.Raw())
			}
			return r.Data
		})
	}

	t := newTable("ПЛОЩАДКА", "СВЯЗЬ", "ВЕРСИЯ", "ДОМЕН", "КАМЕРЫ", "СЕРТИФИКАТ", "ПРИМЕЧАНИЯ")
	for _, r := range results {
		if r.Err != nil {
			t.add(r.Site.Name, "нет", "—", "—", "—", "—", r.Err.Error())
			continue
		}
		st, ok := r.Data.(*Status)
		if !ok {
			t.add(r.Site.Name, "нет", "—", "—", "—", "—", "неожиданный ответ площадки")
			continue
		}

		ready, enabled := st.CamerasReady()
		notes := append([]string(nil), st.Warnings...)
		if !st.Stream.Running {
			notes = append([]string{"поток не запущен: " + firstNonEmpty(st.Stream.LastError, "причина не указана")}, notes...)
		}

		t.add(
			r.Site.Name,
			"ok",
			firstNonEmpty(st.Version, "—"),
			firstNonEmpty(st.Domain, "—"),
			fmt.Sprintf("%d/%d", ready, enabled),
			certCell(st),
			strings.Join(notes, "; "),
		)
	}
	t.write(w)
	return nil
}

func certCell(st *Status) string {
	if !st.Certificate.Available {
		return "нет"
	}
	s := strconv.Itoa(st.Certificate.DaysLeft) + " дн."
	if st.Certificate.SelfSign {
		s += ", свой"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// RenderActions reports a change per site and returns the number of failures.
// Every site gets a line whether it worked or not: a fleet command that printed
// only problems would leave the operator guessing how many sites it reached.
func RenderActions(w io.Writer, results []Result, asJSON bool) error {
	if asJSON {
		return writeJSON(w, results, func(r Result) any { return r.Data })
	}

	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(w, "%s  ОШИБКА: %v\n", r.Site.Name, r.Err)
			continue
		}
		if msg, ok := r.Data.(string); ok && msg != "" {
			fmt.Fprintf(w, "%s  %s\n", r.Site.Name, msg)
			continue
		}
		fmt.Fprintf(w, "%s  готово\n", r.Site.Name)
	}

	failed := Failures(results)
	switch {
	case failed == 0:
		fmt.Fprintf(w, "\nплощадок: %d, все отработали\n", len(results))
	default:
		fmt.Fprintf(w, "\nплощадок: %d, отработали %d, ошибок %d\n",
			len(results), len(results)-failed, failed)
	}
	return nil
}

// jsonResult is the machine-readable shape of one site's outcome. Scripts key
// off ok rather than off the exit code, which only reports the run as a whole.
type jsonResult struct {
	Site  string `json:"site"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func writeJSON(w io.Writer, results []Result, data func(Result) any) error {
	out := make([]jsonResult, 0, len(results))
	for _, r := range results {
		jr := jsonResult{Site: r.Site.Name, OK: r.Err == nil}
		if r.Err != nil {
			jr.Error = r.Err.Error()
		} else {
			jr.Data = data(r)
		}
		out = append(out, jr)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// RenderInventory lists the fleet without printing tokens: the inventory is
// often shown on a shared screen, and the token is the only thing standing
// between a viewer and fifty cameras.
func RenderInventory(w io.Writer, inv *Inventory, asJSON bool) error {
	if asJSON {
		type row struct {
			Name     string   `json:"name"`
			Addr     string   `json:"addr"`
			Tags     []string `json:"tags,omitempty"`
			Note     string   `json:"note,omitempty"`
			Pinned   bool     `json:"pinned"`
			HasToken bool     `json:"has_token"`
			Domain   string   `json:"domain,omitempty"`
			Cameras  int      `json:"cameras"`
			Enabled  int      `json:"cameras_enabled"`
		}
		rows := make([]row, 0, len(inv.Sites))
		for _, s := range inv.Sites {
			enabled, total := s.CameraCount()
			rows = append(rows, row{
				Name: s.Name, Addr: s.Addr, Tags: s.Tags, Note: s.Note,
				Pinned: s.Fingerprint != "", HasToken: s.Token != "",
				Domain: s.Settings.String("domain"), Cameras: total, Enabled: enabled,
			})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(inv.Sites) == 0 {
		fmt.Fprintf(w, "инвентарь пуст: %s\n", inv.Path())
		return nil
	}

	t := newTable("ПЛОЩАДКА", "АДРЕС", "ТОКЕН", "ДОМЕН", "КАМЕРЫ", "МЕТКИ", "ЗАМЕТКА")
	for _, s := range inv.Sites {
		token := "***"
		if s.Token == "" {
			token = "нет"
		}

		// The recorded settings are the point of the file, so the listing says
		// what is in them rather than only how to reach the site.
		domain := firstNonEmpty(s.Settings.String("domain"), "—")
		cams := "—"
		if enabled, total := s.CameraCount(); total > 0 {
			cams = fmt.Sprintf("%d/%d", enabled, total)
		}

		t.add(s.Name, s.Addr, token, domain, cams, strings.Join(s.Tags, ","), s.Note)
	}
	t.write(w)
	fmt.Fprintf(w, "\nвсего площадок: %d (%s)\n", len(inv.Sites), inv.Path())
	return nil
}
