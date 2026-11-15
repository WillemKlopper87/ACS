-- Sign-in attempt limits shared by every replica
-- (docs/SECURITY-ASSESSMENT-2026-11-14.md, O10).
--
-- The password and two-step sign-in limiters count in each process's
-- memory, so N replicas of the operator API allowed N times the attempts.
-- This table is the count they share: one row per key and time window.
-- The key is a digest of what is being limited (a username and address),
-- not the values themselves.
CREATE TABLE auth_attempt_windows (
    key_digest   TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_digest, window_start)
);
CREATE INDEX auth_attempt_windows_start_idx ON auth_attempt_windows (window_start);
