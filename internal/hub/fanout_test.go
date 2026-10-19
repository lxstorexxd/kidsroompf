package hub

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Results must come back in inventory order whoever answers first: a table of
// fifty rows has to read the same way on every run.
func TestRunPreservesOrder(t *testing.T) {
	sites := []Site{
		{Name: "медленная"}, {Name: "быстрая"}, {Name: "средняя"},
	}
	delay := map[string]time.Duration{
		"медленная": 60 * time.Millisecond,
		"быстрая":   0,
		"средняя":   30 * time.Millisecond,
	}

	results := Run(sites, 3, false, func(s Site) (any, error) {
		time.Sleep(delay[s.Name])
		return s.Name, nil
	})

	got := make([]string, 0, len(results))
	for _, r := range results {
		got = append(got, r.Site.Name)
	}
	if strings.Join(got, ",") != "медленная,быстрая,средняя" {
		t.Errorf("порядок результатов: %v", got)
	}
}

// One broken site must not cost the report on the other forty-nine.
func TestRunKeepsGoingAfterFailure(t *testing.T) {
	sites := []Site{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	boom := errors.New("не отвечает")

	results := Run(sites, 3, false, func(s Site) (any, error) {
		if s.Name == "b" {
			return nil, boom
		}
		return "ok", nil
	})

	if Failures(results) != 1 {
		t.Fatalf("ошибок %d, ожидалась одна", Failures(results))
	}
	if !errors.Is(results[1].Err, boom) {
		t.Errorf("ошибка не на своей площадке: %+v", results)
	}
	if results[0].Err != nil || results[2].Err != nil {
		t.Error("остальные площадки должны были отработать")
	}
}

// With --stop-on-error the run must stop at the first failure and say plainly
// that the rest were not touched: better to find out on the first nursery than
// on all fifty.
func TestRunStopOnError(t *testing.T) {
	sites := []Site{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	var visited []string
	var mu sync.Mutex

	results := Run(sites, 4, true, func(s Site) (any, error) {
		mu.Lock()
		visited = append(visited, s.Name)
		mu.Unlock()
		if s.Name == "b" {
			return nil, errors.New("отказ")
		}
		return "ok", nil
	})

	if strings.Join(visited, ",") != "a,b" {
		t.Errorf("обойдены площадки %v, ожидались только a и b", visited)
	}
	for _, name := range []string{"c", "d"} {
		var found bool
		for _, r := range results {
			if r.Site.Name == name {
				found = true
				if !errors.Is(r.Err, ErrSkipped) {
					t.Errorf("площадка %s должна быть помечена как пропущенная, получено %v", name, r.Err)
				}
			}
		}
		if !found {
			t.Errorf("площадка %s пропала из отчёта", name)
		}
	}
	if Failures(results) != 3 {
		t.Errorf("ошибок %d: отказ плюс две пропущенные", Failures(results))
	}
}

func TestRunRespectsParallelLimit(t *testing.T) {
	sites := make([]Site, 20)
	for i := range sites {
		sites[i] = Site{Name: string(rune('a' + i))}
	}

	var mu sync.Mutex
	var now, peak int

	Run(sites, 4, false, func(Site) (any, error) {
		mu.Lock()
		now++
		if now > peak {
			peak = now
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		now--
		mu.Unlock()
		return nil, nil
	})

	if peak > 4 {
		t.Errorf("одновременно работало %d площадок при --parallel 4", peak)
	}
	if peak < 2 {
		t.Errorf("параллелизма не было вовсе (пик %d): пятьдесят площадок по очереди — это минуты", peak)
	}
}

func TestRunOnEmptySelection(t *testing.T) {
	if got := Run(nil, 8, false, func(Site) (any, error) { return nil, nil }); len(got) != 0 {
		t.Errorf("получено %d результатов на пустой выборке", len(got))
	}
}
