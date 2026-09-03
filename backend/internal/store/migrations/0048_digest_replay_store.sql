-- Remediation P1.6 (ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md
-- §6): the CWMP Digest nonce/replay cache was process-local, so it gave
-- no real replay protection behind a load balancer with more than one
-- cmd/acs replica, or across a restart. This table backs
-- internal/auth.PostgresReplayStore -- the shared-storage architecture
-- chosen because this codebase already treats Postgres as the source
-- of truth for every other piece of cross-replica CWMP state.
--
-- last_nc doubles for both replay semantics a Digest response can use:
-- qop=auth's nc must strictly increase per nonce, and a legacy non-qop
-- response is single-use -- modeled by the caller as "nc must exceed 1",
-- so a first insert at last_nc=1 and any later attempt at <=1 both fall
-- out of the same "nc must strictly increase" check with no separate
-- code path.
CREATE TABLE cwmp_digest_nonces (
    nonce TEXT PRIMARY KEY,
    last_nc BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- Serves the reaper's periodic purge of expired rows; without one that
-- query would be a full scan over an ever-growing table.
CREATE INDEX cwmp_digest_nonces_expires_at_idx ON cwmp_digest_nonces(expires_at);
