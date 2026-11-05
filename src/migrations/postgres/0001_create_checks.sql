CREATE TABLE checks (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    url               TEXT        NOT NULL,
    url_hash          CHAR(64)    NOT NULL,
    final_url         TEXT,
    browser_final_url TEXT,
    status            VARCHAR(16) NOT NULL DEFAULT 'pending',
    error_code        VARCHAR(32),
    error_message     TEXT,
    http_status_code  INTEGER,
    is_available      BOOLEAN,
    response_time_ms  BIGINT,
    redirect_count    INTEGER     NOT NULL DEFAULT 0,
    redirect_chain    JSONB,
    metadata_status   VARCHAR(16) NOT NULL DEFAULT 'pending',
    title             TEXT,
    description       TEXT,
    screenshot_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    screenshot_error  TEXT,
    screenshot_path   TEXT,
    attempts          INTEGER     NOT NULL DEFAULT 0,
    locked_until      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ
);

CREATE INDEX idx_checks_status_created ON checks (status, created_at, id);
CREATE INDEX idx_checks_status_locked ON checks (status, locked_until);
CREATE INDEX idx_checks_url_hash ON checks (url_hash);
CREATE INDEX idx_checks_created ON checks (created_at, id);
