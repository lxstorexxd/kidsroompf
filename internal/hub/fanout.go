package hub

import (
	"errors"
	"sync"
)

// ErrSkipped marks a site the run never reached because an earlier one failed.
var ErrSkipped = errors.New("пропущена: предыдущая площадка ответила ошибкой")

// Result is what one site answered.
type Result struct {
	Site Site
	Data any
	Err  error
}

// Run applies fn to every site and returns the results in the order the sites
// were given, so a table of fifty rows reads the same way every time regardless
// of who answered first.
//
// With stopOnError the sites are visited one at a time and the run stops at the
// first failure, leaving the rest marked as skipped. That is the mode for rolling
// out a change the operator is not yet sure about: better to find out on the
// first nursery than on all fifty. Otherwise one failing site never holds up the
// others — its error is carried in its own Result and reported next to its name.
func Run(sites []Site, parallel int, stopOnError bool, fn func(Site) (any, error)) []Result {
	if stopOnError {
		return runSequential(sites, fn)
	}
	return runParallel(sites, parallel, fn)
}

func runSequential(sites []Site, fn func(Site) (any, error)) []Result {
	out := make([]Result, len(sites))
	for i, s := range sites {
		data, err := fn(s)
		out[i] = Result{Site: s, Data: data, Err: err}
		if err != nil {
			for j := i + 1; j < len(sites); j++ {
				out[j] = Result{Site: sites[j], Err: ErrSkipped}
			}
			break
		}
	}
	return out
}

func runParallel(sites []Site, parallel int, fn func(Site) (any, error)) []Result {
	if parallel < 1 {
		parallel = 1
	}
	if parallel > len(sites) {
		parallel = len(sites)
	}

	out := make([]Result, len(sites))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, s := range sites {
		wg.Add(1)
		go func(i int, s Site) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := fn(s)
			out[i] = Result{Site: s, Data: data, Err: err}
		}(i, s)
	}
	wg.Wait()
	return out
}

// Failures counts the sites that did not do what was asked.
func Failures(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Err != nil {
			n++
		}
	}
	return n
}
