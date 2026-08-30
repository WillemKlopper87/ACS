-- Audit P1.1: a job leased into RPC_SENT / IN_PROGRESS recorded no
-- lease owner or deadline, so a gateway crash between lease and
-- completion stranded the job forever. Every lease now carries an owner
-- and an expiry; jobs.RecoverExpiredLeases (run by cmd/acs) requeues
-- expired work while attempts remain and dead-letters it (FAILED) when
-- they don't. Long-running work extends its lease via ExtendLease.
ALTER TABLE jobs ADD COLUMN lease_owner TEXT;
ALTER TABLE jobs ADD COLUMN leased_until TIMESTAMPTZ;
CREATE INDEX jobs_status_leased_until_idx ON jobs (status, leased_until);
