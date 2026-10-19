package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInventory(t *testing.T, sites ...Site) *Inventory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inventory.json")
	inv, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	inv.Sites = sites
	return inv
}

func fleet() []Site {
	return []Site{
		{Name: "sad5", Addr: "https://10.8.0.15:8081", Token: "t5", Tags: []string{"msk", "wave1"}},
		{Name: "sad7", Addr: "https://10.8.0.17:8081", Token: "t7", Tags: []string{"msk"}},
		{Name: "sad12", Addr: "http://10.8.0.22:8081", Token: "t12", Tags: []string{"spb", "wave1"}},
	}
}

func names(sites []Site) string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Name)
	}
	return strings.Join(out, ",")
}

func TestResolveSelection(t *testing.T) {
	inv := testInventory(t, fleet()...)

	cases := []struct {
		name string
		sel  Selection
		want string
	}{
		{"одно имя", Selection{Names: []string{"sad7"}}, "sad7"},
		{"несколько имён", Selection{Names: []string{"sad12", "sad5"}}, "sad5,sad12"},
		{"все", Selection{All: true}, "sad5,sad7,sad12"},
		{"по метке", Selection{Tags: []string{"msk"}}, "sad5,sad7"},
		{"две метки объединяются", Selection{Tags: []string{"spb", "wave1"}}, "sad5,sad12"},
		{"метка и имя", Selection{Names: []string{"sad7"}, Tags: []string{"spb"}}, "sad7,sad12"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := inv.Resolve(c.sel)
			if err != nil {
				t.Fatal(err)
			}
			// The order must follow the inventory, so a table of fifty rows
			// reads the same way on every run.
			if names(got) != c.want {
				t.Errorf("получено %q, ожидалось %q", names(got), c.want)
			}
		})
	}
}

// Doing less than asked is how a certificate quietly fails to reach half the
// fleet, so every one of these is an error rather than an empty selection.
func TestResolveRejects(t *testing.T) {
	inv := testInventory(t, fleet()...)

	cases := map[string]Selection{
		"неизвестное имя":       {Names: []string{"sad99"}},
		"метка ни на кого":      {Tags: []string{"nsk"}},
		"--all вместе с именем": {All: true, Names: []string{"sad5"}},
		"--all вместе с меткой": {All: true, Tags: []string{"msk"}},
		"пустая выборка":        {},
	}
	for name, sel := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := inv.Resolve(sel); err == nil {
				t.Fatal("ожидалась ошибка, её нет")
			}
		})
	}
}

func TestResolveOnEmptyInventory(t *testing.T) {
	inv := testInventory(t)
	if _, err := inv.Resolve(Selection{All: true}); err == nil {
		t.Fatal("пустой инвентарь должен давать внятную ошибку, а не пустой список")
	}
}

func TestSiteValidate(t *testing.T) {
	good := Site{Name: "sad5", Addr: "https://10.8.0.15:8081", Token: "t"}
	if err := good.Validate(); err != nil {
		t.Fatalf("корректная площадка отклонена: %v", err)
	}

	// An empty token is deliberately allowed: while a fleet is being provisioned
	// the inventory legitimately holds sites whose token has not been collected
	// yet, and list shows "нет" against each of them as a checklist.
	tokenless := good
	tokenless.Token = ""
	if err := tokenless.Validate(); err != nil {
		t.Errorf("площадка без токена должна проходить проверку: %v", err)
	}

	cases := map[string]func(*Site){
		"имя с пробелом":      func(s *Site) { s.Name = "sad 5" },
		"имя в верхнем":       func(s *Site) { s.Name = "Sad5" },
		"адрес без схемы":     func(s *Site) { s.Addr = "10.8.0.15:8081" },
		"чужая схема":         func(s *Site) { s.Addr = "ftp://10.8.0.15:8081" },
		"адрес с путём":       func(s *Site) { s.Addr = "https://10.8.0.15:8081/api" },
		"метка с запрещённым": func(s *Site) { s.Tags = []string{"Москва"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := good
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("ожидалась ошибка валидации, её нет")
			}
		})
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	inv, err := Load(filepath.Join(t.TempDir(), "нет-такого.json"))
	if err != nil {
		t.Fatalf("отсутствующий файл должен давать пустой инвентарь: %v", err)
	}
	if len(inv.Sites) != 0 || inv.Version != InventoryVersion {
		t.Errorf("получено %+v", inv)
	}
}

// Notepad and PowerShell both write a BOM, and a BOM must not cost the operator
// an evening.
func TestLoadAcceptsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	body := `{"version":1,"sites":[{"name":"sad5","addr":"https://10.8.0.15:8081","token":"t"}]}`
	if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), body...), 0o600); err != nil {
		t.Fatal(err)
	}

	inv, err := Load(path)
	if err != nil {
		t.Fatalf("инвентарь с BOM не прочитан: %v", err)
	}
	if len(inv.Sites) != 1 || inv.Sites[0].Name != "sad5" {
		t.Errorf("получено %+v", inv.Sites)
	}
}

func TestLoadRejectsDuplicateNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	body := `{"version":1,"sites":[
		{"name":"sad5","addr":"https://10.8.0.15:8081","token":"a"},
		{"name":"sad5","addr":"https://10.8.0.16:8081","token":"b"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("две площадки с одним именем должны отвергаться: иначе непонятно, к какой придёт команда")
	}
}

func TestLoadRejectsNewerFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"sites":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("формат из будущего должен отвергаться, а не разбираться наугад")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	inv, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.Add(Site{Name: "sad5", Addr: "https://10.8.0.15:8081", Token: "t5", Tags: []string{"msk"}, Note: "Садик №5"}); err != nil {
		t.Fatal(err)
	}
	if err := inv.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Sites) != 1 {
		t.Fatalf("после перечитывания площадок %d", len(again.Sites))
	}
	got := again.Sites[0]
	if got.Token != "t5" || got.Note != "Садик №5" || len(got.Tags) != 1 {
		t.Errorf("площадка не совпадает: %+v", got)
	}

	// Nothing may be left behind: a stray temp file in the same directory would
	// be a token lying around in the clear.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".inventory-") {
			t.Errorf("остался временный файл %s", e.Name())
		}
	}
}

func TestAddRejectsDuplicateAndUpdateKeepsOrder(t *testing.T) {
	inv := testInventory(t, fleet()...)

	if err := inv.Add(Site{Name: "sad7", Addr: "https://10.8.0.99:8081", Token: "x"}); err == nil {
		t.Error("повторное добавление должно отвергаться")
	}

	s, _ := inv.Find("sad7")
	s.Token = "новый"
	if err := inv.Update(s); err != nil {
		t.Fatal(err)
	}
	if names(inv.Sites) != "sad5,sad7,sad12" {
		t.Errorf("порядок площадок изменился: %s", names(inv.Sites))
	}
	if got, _ := inv.Find("sad7"); got.Token != "новый" {
		t.Errorf("токен не обновлён: %q", got.Token)
	}
}

func TestRemove(t *testing.T) {
	inv := testInventory(t, fleet()...)
	if err := inv.Remove("sad7"); err != nil {
		t.Fatal(err)
	}
	if names(inv.Sites) != "sad5,sad12" {
		t.Errorf("получено %s", names(inv.Sites))
	}
	if err := inv.Remove("sad7"); err == nil {
		t.Error("повторное удаление должно сообщать, что площадки нет")
	}
}

func TestHasTagIgnoresCase(t *testing.T) {
	s := Site{Tags: []string{"msk"}}
	if !s.HasTag("MSK") || s.HasTag("spb") {
		t.Error("метка должна сравниваться без учёта регистра")
	}
}

// Creating inventory.json by hand before running add leaves a zero-byte file.
// An empty file is not valid JSON, so without this it would block add — the one
// command meant to fill it.
func TestLoadAcceptsEmptyFile(t *testing.T) {
	for name, body := range map[string]string{
		"пустой файл":    "",
		"только пробелы": "  \r\n\t ",
		"только BOM":     "\xef\xbb\xbf",
		"пустой объект":  "{}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inventory.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			inv, err := Load(path)
			if err != nil {
				t.Fatalf("файл не прочитан: %v", err)
			}
			if len(inv.Sites) != 0 || inv.Version != InventoryVersion {
				t.Errorf("получено %+v", inv)
			}

			// And add must work on it, which is the whole point.
			if err := inv.Add(Site{Name: "sad5", Addr: "https://10.0.0.1:8081", Token: "t"}); err != nil {
				t.Fatal(err)
			}
			if err := inv.Save(); err != nil {
				t.Fatal(err)
			}
			again, err := Load(path)
			if err != nil || len(again.Sites) != 1 {
				t.Errorf("после записи: %v, %v", again, err)
			}
		})
	}
}
