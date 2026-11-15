-- Operator MFA (docs/CONSOLE-REDESIGN-AND-MFA.md §5): a time-based one-time
-- code (TOTP, RFC 6238) from an authenticator app as a second step after
-- the password.
--
-- secret_enc is the shared secret sealed with AES-256-GCM under a key
-- derived from ACS_CREDENTIAL_ENCRYPTION_KEY, with the operator id as
-- additional data so a sealed secret cannot be moved to another operator.
-- confirmed_at stays NULL until the operator proves the app works with a
-- first code: an unconfirmed row never gates a login.
-- last_used_step is the 30-second step of the last accepted code, so a
-- code cannot be used twice.
CREATE TABLE operator_mfa (
    operator_id    UUID PRIMARY KEY REFERENCES operators(id) ON DELETE CASCADE,
    secret_enc     TEXT NOT NULL,
    confirmed_at   TIMESTAMPTZ,
    last_used_step BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One-time recovery codes for a lost phone. Only a hash is kept; a used
-- code stays as a row so the count of codes left is honest.
CREATE TABLE operator_mfa_recovery_codes (
    operator_id UUID NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    code_hash   TEXT NOT NULL,
    used_at     TIMESTAMPTZ,
    PRIMARY KEY (operator_id, code_hash)
);
