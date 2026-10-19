package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// The operator types flags and site names in whatever order reads naturally,
// and every one of these orders has to mean the same thing.
func TestParseInterleaved(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantPos string
		wantAll bool
		wantYes bool
		wantTag string
	}{
		"только имена": {
			args: []string{"sad5", "sad7"}, wantPos: "sad5,sad7",
		},
		"флаги впереди": {
			args: []string{"--all", "--yes"}, wantAll: true, wantYes: true,
		},
		"флаг, позиционный, флаг": {
			args:    []string{"--all", "patch.json", "--yes"},
			wantPos: "patch.json", wantAll: true, wantYes: true,
		},
		"позиционные, затем флаги": {
			args:    []string{"sad5", "cam1", "--yes"},
			wantPos: "sad5,cam1", wantYes: true,
		},
		"флаг со значением между позиционными": {
			args:    []string{"sad5", "--tag", "msk", "sad7", "--yes"},
			wantPos: "sad5,sad7", wantTag: "msk", wantYes: true,
		},
		"значение через знак равенства": {
			args:    []string{"--tag=msk", "file.json"},
			wantPos: "file.json", wantTag: "msk",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var all, yes bool
			var tags tagList

			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			fs.BoolVar(&all, "all", false, "")
			fs.BoolVar(&yes, "yes", false, "")
			fs.Var(&tags, "tag", "")

			pos, err := parseInterleaved(fs, c.args)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(pos, ","); got != c.wantPos {
				t.Errorf("позиционные %q, ожидались %q", got, c.wantPos)
			}
			if all != c.wantAll || yes != c.wantYes {
				t.Errorf("--all=%v --yes=%v, ожидалось %v/%v", all, yes, c.wantAll, c.wantYes)
			}
			if got := strings.Join(tags, ","); got != c.wantTag {
				t.Errorf("--tag %q, ожидалось %q", got, c.wantTag)
			}
		})
	}
}

func TestParseInterleavedReportsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if _, err := parseInterleaved(fs, []string{"sad5", "--нет-такого"}); err == nil {
		t.Error("неизвестный флаг должен быть ошибкой, а не позиционным аргументом")
	}
}

func TestTagListAcceptsCommasAndRepeats(t *testing.T) {
	var tags tagList
	if err := tags.Set("msk, spb"); err != nil {
		t.Fatal(err)
	}
	if err := tags.Set("wave1"); err != nil {
		t.Fatal(err)
	}
	if got := tags.String(); got != "msk,spb,wave1" {
		t.Errorf("получено %q", got)
	}
}

func TestSplitTail(t *testing.T) {
	names, tail, err := splitTail([]string{"sad5", "sad7", "domain.ru"}, "домен")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "sad5,sad7" || tail != "domain.ru" {
		t.Errorf("names=%v tail=%q", names, tail)
	}

	// With --all or --tag there are no names, only the trailing value.
	names, tail, err = splitTail([]string{"domain.ru"}, "домен")
	if err != nil || len(names) != 0 || tail != "domain.ru" {
		t.Errorf("names=%v tail=%q err=%v", names, tail, err)
	}

	if _, _, err := splitTail(nil, "домен"); err == nil {
		t.Error("отсутствие аргумента должно давать внятную ошибку")
	}
}
