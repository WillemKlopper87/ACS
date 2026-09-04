-- UI/UX review 2026-09-04 P1.4: an operator account could be created and
-- have its password reset, but never taken out of service. Offboarding
-- somebody who left meant either deleting the row -- which orphans every
-- audit_log entry attributing an action to them -- or rotating their
-- password and trusting nobody had the old one. Neither is an answer.
--
-- Disabling is the answer: the row stays, so the audit trail keeps
-- resolving, and the account stops being usable. Nullable timestamp
-- rather than a boolean so "when were they offboarded" is answerable
-- from the same column, which is exactly what an auditor asks.
ALTER TABLE operators ADD COLUMN disabled_at TIMESTAMPTZ;

COMMENT ON COLUMN operators.disabled_at IS
    'When this operator was taken out of service (audit 2026-09-04 P1.4). NULL means active. A disabled operator cannot log in, and disabling bumps token_version so any session they already hold is revoked immediately rather than surviving until its JWT expires.';

CREATE INDEX idx_operators_active ON operators (username) WHERE disabled_at IS NULL;
