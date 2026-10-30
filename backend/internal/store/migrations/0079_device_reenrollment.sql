-- Operator re-enrolment window (field test 2026-10-30). A device is
-- "established" once it has informed, and established devices may never use
-- the constrained bootstrap identity again. A factory-reset CPE comes back
-- with the bootstrap identity and was therefore locked out permanently.
-- While reenroll_until is in the future, bootstrap graduation is allowed for
-- this one device; successful graduation clears it.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS reenroll_until TIMESTAMPTZ;
