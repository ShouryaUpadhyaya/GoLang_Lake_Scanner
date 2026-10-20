package pool

import (
	"context"
	"sync"
	"time"
)

type Result[I, O any] struct {
	Input  I
	Output O
	Err    error
}

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

	go func() {
		defer close(jobs)
		for _, in := range inputs {
			select {
			case jobs <- in:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range jobs {
				results <- runOne(ctx, timeout, in, fn)
			}
		}()
	}

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
	defer cancel()

	out, err := fn(jobCtx, in)
	return Result[I, O]{Input: in, Output: out, Err: err}
}
