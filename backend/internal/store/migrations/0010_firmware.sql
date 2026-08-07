-- Phase 4 (build plan §4 Phase 4 / design doc v3 §7.6, §9). Firmware
-- binaries themselves are NOT stored here — v3 §9.4/§19.4 is explicit
-- that images live outside the relational database, served over
-- HTTP(S). This table is metadata only; the bytes live on local disk
-- under internal/firmware's storage root, a deliberate stand-in for the
-- S3/MinIO/CDN v3 actually specifies (see internal/firmware doc comment
-- for why, and what a real deployment needs instead).
--
-- Deliberately NOT built in this migration: firmware_rollout /
-- firmware_rollout_device (v3 §9.5 — canary_percentage, maintenance
-- windows, rollback). v3's own MVP definition (§17) puts the rollout/
-- canary engine in "Later," not "Must have" — this migration covers
-- single-device Download/TransferComplete only, matching that scope.
CREATE TABLE firmware_images (
    id UUID PRIMARY KEY,
    vendor TEXT NOT NULL,
    model TEXT NOT NULL,
    version TEXT NOT NULL,
    channel TEXT NOT NULL DEFAULT 'stable',
    filename TEXT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (vendor, model, version, channel)
);

ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST', 'FIRMWARE_DOWNLOAD'));

-- AWAITING_TRANSFER_COMPLETE: a FIRMWARE_DOWNLOAD job sits here between a
-- successful DownloadResponse and the eventual TransferComplete — v3
-- §9.2 is explicit that DownloadResponse only means the CPE *accepted*
-- the request, not that the transfer finished. Treating that as SUCCESS
-- would be the exact "don't trust the ack" mistake v3 §19.7 warns about.
ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('QUEUED', 'RPC_SENT', 'IN_PROGRESS', 'AWAITING_TRANSFER_COMPLETE', 'SUCCESS', 'FAILED', 'TIMEOUT'));
