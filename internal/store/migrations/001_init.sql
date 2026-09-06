CREATE TABLE builds (
    id                TEXT PRIMARY KEY,
    owner             TEXT NOT NULL,
    repo              TEXT NOT NULL,
    sha               TEXT NOT NULL,
    status            TEXT NOT NULL,
    kind              TEXT NOT NULL DEFAULT '',
    recipe            JSONB,
    port              INT NOT NULL DEFAULT 0,
    golden_sandbox_id TEXT NOT NULL DEFAULT '',
    snapshot_id       TEXT NOT NULL DEFAULT '',
    log               TEXT NOT NULL DEFAULT '',
    error             TEXT NOT NULL DEFAULT '',
    built_at          TIMESTAMPTZ,
    last_visit_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    visits            INT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX builds_repo_sha ON builds (owner, repo, sha);
CREATE INDEX builds_repo_created ON builds (owner, repo, created_at DESC);
CREATE INDEX builds_last_visit ON builds (last_visit_at) WHERE status = 'ready';

CREATE TABLE sessions (
    id             TEXT PRIMARY KEY,
    build_id       TEXT NOT NULL REFERENCES builds(id),
    device         TEXT NOT NULL,
    ip             TEXT NOT NULL,
    sandbox_id     TEXT NOT NULL DEFAULT '',
    preview_url    TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL,
    terminal_ready BOOLEAN NOT NULL DEFAULT false,
    extended       BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ,
    ended_reason   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_live_device ON sessions (device) WHERE status <> 'ended';
CREATE INDEX sessions_live_ip ON sessions (ip) WHERE status <> 'ended';

CREATE TABLE usage_daily (
    day   TEXT NOT NULL,
    kind  TEXT NOT NULL,
    owner TEXT NOT NULL,
    count INT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, kind, owner)
);
