-- Request-rate counts shared by every replica of a service
-- (docs/SECURITY-ASSESSMENT-2026-11-14.md, O10). Used only when
-- ACS_SHARED_RATE_LIMIT=true: each replica adds its counts about once a
-- second and learns which callers are over the limit across all of them.
-- The key is a digest of the caller (an operator, a client, an address).
CREATE TABLE rate_limit_windows (
    scope        TEXT NOT NULL,
    key_digest   TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    hits         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, key_digest, window_start)
);
CREATE INDEX rate_limit_windows_start_idx ON rate_limit_windows (scope, window_start);
