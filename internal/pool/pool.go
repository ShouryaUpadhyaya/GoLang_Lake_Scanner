// Package pool runs a function over many inputs with a fixed number of
// goroutines ("workers").
package pool

import (
	"context"
	"sync"
	"time"
)

// Result pairs an input with what happened to it. [I, O any] makes this a
// generic type: I and O are placeholders filled in by the caller, so the
// pool works for any input/output types.
type Result[I, O any] struct {
	Input  I
	Output O
	Err    error // timeout, cancellation, or whatever fn returned
}

// Run processes every input with `workers` goroutines and streams results on
// the returned channel, which is closed when all work is done.
//
// Each call to fn gets its own context that expires after `timeout`.
// One failing job never stops the others; its error rides along in Result.
// Cancelling ctx stops handing out new jobs and cancels the ones in flight.
func Run[I, O any](
	ctx context.Context,
	workers int,
	timeout time.Duration,
	inputs []I,
	fn func(context.Context, I) (O, error),
) <-chan Result[I, O] {
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan I)
	results := make(chan Result[I, O])

	// 1. Feeder: the only sender on `jobs`, so it is the one that closes it.
	go func() {
		defer close(jobs)
		for _, in := range inputs {
			select {
			case jobs <- in:
			case <-ctx.Done(): // stop early on Ctrl-C
				return
			}
		}
	}()

	// 2. Workers. wg.Add happens BEFORE `go`, in the loop that starts them.
	//    If Add ran inside the goroutine, wg.Wait() below could run first,
	//    see a zero counter, and close `results` too early.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range jobs { // loop ends when jobs is closed and drained
				results <- runOne(ctx, timeout, in, fn)
			}
		}()
	}

	// 3. Coordinator: waits for every worker, then closes `results`.
	//    Workers must not close it themselves: the first to finish would
	//    close it while the others are still sending (panic).
	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

func runOne[I, O any](
	ctx context.Context,
	timeout time.Duration,
	in I,
	fn func(context.Context, I) (O, error),
) Result[I, O] {
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel() // always release the timer, even on success

	out, err := fn(jobCtx, in)
	return Result[I, O]{Input: in, Output: out, Err: err}
}
