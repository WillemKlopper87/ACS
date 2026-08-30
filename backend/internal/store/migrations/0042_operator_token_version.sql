-- JWT revocation (audit "missing checks": revocation/rotation). Every
-- operator JWT carries the token_version it was issued under; bumping
-- the column (password change, reset, explicit logout) invalidates all
-- of that operator's outstanding tokens without a server-side session
-- store or a denylist.
ALTER TABLE operators ADD COLUMN token_version INTEGER NOT NULL DEFAULT 1;
