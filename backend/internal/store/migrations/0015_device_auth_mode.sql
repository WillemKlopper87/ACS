-- Phase 6 (build plan §4 Phase 6 / design doc v3 §11.2: "mTLS preferred,
-- Digest fallback ... the implementation must support both and record
-- per-device auth mode"). Recorded on every Inform, not assumed from
-- config — a fleet with prerequisite P3 unresolved per-vendor (v3 §13)
-- may have some devices doing mTLS and others still on Digest
-- simultaneously against the same endpoint.
ALTER TABLE devices ADD COLUMN cwmp_auth_mode TEXT NOT NULL DEFAULT 'NONE'
    CHECK (cwmp_auth_mode IN ('MTLS', 'DIGEST', 'NONE'));
