package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lake-scanner/internal/catalog"
	"lake-scanner/internal/classify"
	"lake-scanner/internal/pool"
	"lake-scanner/internal/scanner"
)

const defaultDB = "postgres://shouryaupadhyaya@/lake_scanner?host=/run/postgresql"

type line struct {
	scanner.FileRecord
	State catalog.State `json:"state"`
}

func main() {
	workers := flag.Int("workers", 4, "number of concurrent classifier workers")
	timeout := flag.Duration("timeout", 30*time.Second, "time limit per file")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: lake-scanner [-workers N] [-timeout 30s] <root-directory>")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, flag.Arg(0), *workers, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, root string, workers int, timeout time.Duration) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = defaultDB
	}
	cat, err := catalog.Open(ctx, url)
	if err != nil {
		return err
	}
	defer cat.Close()

	enc := json.NewEncoder(os.Stdout)
	counts := map[catalog.State]int{}

	err = scanner.Walk(root,
		func(rec scanner.FileRecord) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			state, err := cat.Upsert(ctx, rec.Path, rec.Hash, rec.Size)
			if err != nil {
				return err
			}
			counts[state]++
			return enc.Encode(line{rec, state})
		},
		func(path string, err error) {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", path, err)
		})
	if err != nil {
		return err
	}

	pending, err := cat.Unclassified(ctx)
	if err != nil {
		return err
	}
	var clf classify.Classifier = classify.New()
	classifyOne := func(ctx context.Context, p catalog.Pending) (classify.Result, error) {
		res := clf.Classify(ctx, p.Path)

		return res, ctx.Err()
	}

	var done, failed int
	for r := range pool.Run(ctx, workers, timeout, pending, classifyOne) {
		p := r.Input
		switch {
		case errors.Is(r.Err, context.Canceled):
			continue
		case r.Err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "failed %s: %v\n", p.Path, r.Err)
			if err := cat.SetError(ctx, p.Path, p.Hash, r.Err.Error()); err != nil {
				return err
			}
			continue
		}
		done++
		if err := cat.SetClassification(ctx, p.Path, p.Hash,
			string(r.Output.Kind), r.Output.MIME, r.Output.Detail); err != nil {
			return err
		}
		if err := enc.Encode(struct {
			Path string `json:"path"`
			classify.Result
		}{p.Path, r.Output}); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "scan: %d new, %d changed, %d unchanged; classified %d, failed %d\n",
		counts[catalog.StateNew], counts[catalog.StateChanged], counts[catalog.StateUnchanged], done, failed)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "interrupted: unfinished files will be retried next run")
	}
	return nil
}
