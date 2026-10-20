package catalog

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The compiler copies schema.sql into the binary as a string.
//
//go:embed schema.sql
var schema string

// State says what an Upsert found.
type State string

const (
	StateNew       State = "new"
	StateChanged   State = "changed"
	StateUnchanged State = "unchanged"
)

type Catalog struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Catalog, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return &Catalog{pool: pool}, nil
}

func (c *Catalog) Close() { c.pool.Close() }

// Upsert records a file and reports whether it is new, changed or unchanged.
// New and changed files are (re)set to status 'unclassified'.
func (c *Catalog) Upsert(ctx context.Context, path, hash string, size int64) (State, error) {
	var old string
	err := c.pool.QueryRow(ctx, `SELECT sha256 FROM files WHERE path = $1`, path).Scan(&old)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// ON CONFLICT DO NOTHING keeps this safe if two workers race (M4).
		_, err = c.pool.Exec(ctx,
			`INSERT INTO files (sha256, path, size) VALUES ($1, $2, $3)
			 ON CONFLICT (path) DO NOTHING`, hash, path, size)
		return StateNew, err
	case err != nil:
		return "", err
	case old != hash:
		_, err = c.pool.Exec(ctx,
			`UPDATE files SET sha256 = $2, size = $3, kind = 'unknown',
			        status = 'unclassified', last_seen = now(), updated_at = now()
			 WHERE path = $1`, path, hash, size)
		return StateChanged, err
	default:
		_, err = c.pool.Exec(ctx, `UPDATE files SET last_seen = now() WHERE path = $1`, path)
		return StateUnchanged, err
	}
}

// Pending is a catalog row that still needs classifying.
type Pending struct {
	Path string
	Hash string
}

// Unclassified returns files still needing work: status 'unclassified' or 'error'
// (failed files are retried on the next run). That covers new files,
// files whose hash changed, and anything a crashed earlier run left behind.
func (c *Catalog) Unclassified(ctx context.Context) ([]Pending, error) {
	rows, err := c.pool.Query(ctx,
		`SELECT path, sha256 FROM files WHERE status IN ('unclassified', 'error') ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.Path, &p.Hash); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err() // always check Err() after a rows loop
}

// SetClassification stores the result and marks the file classified. The
// hash guard means that if the file changed meanwhile, we don't overwrite.
func (c *Catalog) SetClassification(ctx context.Context, path, hash, kind, mime, detail string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE files SET kind = $3, mime = $4, detail = $5,
		        status = 'classified', updated_at = now()
		 WHERE path = $1 AND sha256 = $2`, path, hash, kind, mime, detail)
	return err
}

// SetError records that classifying a file failed, so it is visible and will
// be retried on the next run.
func (c *Catalog) SetError(ctx context.Context, path, hash, msg string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE files SET status = 'error', detail = $3, updated_at = now()
		 WHERE path = $1 AND sha256 = $2`, path, hash, msg)
	return err
}
