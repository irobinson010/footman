-- Timestamps are unix milliseconds (UTC).

CREATE TABLE history (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    ts   INTEGER NOT NULL,
    kind TEXT    NOT NULL,  -- transcript | command | ...
    text TEXT    NOT NULL
);
CREATE INDEX history_ts ON history(ts);

CREATE TABLE jobs (
    id         TEXT    PRIMARY KEY,
    kind       TEXT    NOT NULL,
    status     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    input      TEXT    NOT NULL DEFAULT '',
    output     TEXT    NOT NULL DEFAULT '',
    error      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX jobs_status ON jobs(status);

CREATE TABLE action_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    actor        TEXT    NOT NULL,  -- recognized speaker
    action       TEXT    NOT NULL,
    tool         TEXT    NOT NULL,
    args         TEXT    NOT NULL DEFAULT '',  -- secrets already redacted by the caller
    result       TEXT    NOT NULL DEFAULT '',
    confirmation TEXT    NOT NULL DEFAULT '',
    reason       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX action_log_ts ON action_log(ts);

-- Retention pruning opens this gate (inside one transaction) so rows older
-- than the cutoff may be deleted; it is closed again before commit.
CREATE TABLE action_log_prune_gate (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    cutoff INTEGER
);
INSERT INTO action_log_prune_gate (id, cutoff) VALUES (1, NULL);

CREATE TRIGGER action_log_no_update BEFORE UPDATE ON action_log
BEGIN
    SELECT RAISE(ABORT, 'action_log is append-only');
END;

CREATE TRIGGER action_log_no_delete BEFORE DELETE ON action_log
WHEN (SELECT cutoff FROM action_log_prune_gate WHERE id = 1) IS NULL
  OR OLD.ts >= (SELECT cutoff FROM action_log_prune_gate WHERE id = 1)
BEGIN
    SELECT RAISE(ABORT, 'action_log is append-only (only retention pruning may delete)');
END;

CREATE TABLE memory (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
