package hooks

import (
	"runtime"
	"sync"
)

// parallel runs fn(i) for i in [0,n) on up to workers goroutines. fn must
// only write to its own slot; that is what makes listing safe to spread.
func parallel(n, workers int, fn func(i int)) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	next := make(chan int)
	var once sync.Once
	var caught *enginePanic
	// A panic on a worker goroutine would end the process, and no caller
	// can recover it. Each item recovers its own, the first one is kept, and
	// it is raised again on the caller's goroutine once every worker is done.
	guarded := func(i int) {
		defer func() {
			if p := recover(); p != nil {
				ep := capture(p)
				once.Do(func() { caught = ep })
			}
		}()
		fn(i)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				guarded(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	if caught != nil {
		panic(caught)
	}
}
