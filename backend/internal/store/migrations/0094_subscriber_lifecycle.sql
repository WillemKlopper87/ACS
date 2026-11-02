-- Subscriber lifecycle after a sale (internal/lifecycle): a sale in
-- service can be ended (ENDED), which ends its account mappings and
-- releases the device to the unassigned pool. When an account has no
-- device left, its subscriber profile moves to an archive that the
-- retention job purges after ACS_RETENTION_SUBSCRIBER_ARCHIVE_DAYS
-- (default three years).

ALTER TABLE onboardings DROP CONSTRAINT onboardings_state_check;
ALTER TABLE onboardings ADD CONSTRAINT onboardings_state_check CHECK (state IN
    ('PLANNED', 'CONTACTED', 'SECURED', 'DISCOVERED', 'CONFIGURING', 'IN_SERVICE', 'ON_HOLD', 'FAILED', 'CANCELLED', 'ENDED'));
ALTER TABLE onboardings ADD COLUMN ended_at TIMESTAMPTZ;

-- An ended sale is closed like a cancelled one: its serial may be sold
-- again.
DROP INDEX onboardings_open_serial_idx;
CREATE UNIQUE INDEX onboardings_open_serial_idx ON onboardings (expected_serial)
    WHERE state NOT IN ('IN_SERVICE', 'CANCELLED', 'ENDED');

-- Archived subscriber profiles (POPIA): kept only to answer questions
-- about a past customer within the retention period, never shown in
-- lookups or device reads, and deleted by the retention job.
CREATE TABLE account_profile_archive (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   TEXT NOT NULL,
    display_name TEXT,
    name_source  TEXT,
    vip          BOOLEAN NOT NULL,
    reason       TEXT NOT NULL,
    archived_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_by  TEXT NOT NULL
);
CREATE INDEX account_profile_archive_archived_idx ON account_profile_archive (archived_at);
CREATE INDEX account_profile_archive_account_idx ON account_profile_archive (account_id);
