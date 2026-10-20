CREATE TABLE IF NOT EXISTS files (
    id          BIGSERIAL PRIMARY KEY,
    sha256      CHAR(64)    NOT NULL,
    path        TEXT        NOT NULL UNIQUE,
    size        BIGINT      NOT NULL,
    kind        TEXT        NOT NULL DEFAULT 'unknown',
    status      TEXT        NOT NULL DEFAULT 'unclassified',
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS files_sha256_idx ON files (sha256);

ALTER TABLE files ADD COLUMN IF NOT EXISTS mime   TEXT;
ALTER TABLE files ADD COLUMN IF NOT EXISTS detail TEXT;
