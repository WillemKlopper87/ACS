-- Remediation P0.1 (ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md): zero
-- operator_scopes rows must mean zero tenant/device access, not unrestricted
-- access. Before this migration, an operator with no scope rows saw every
-- customer -- a manager/NOC operator created without scopes, or one whose
-- last scope row was removed, silently had fleet-wide access. That default
-- is inverted from here on: unrestricted access now requires this explicit
-- flag, set deliberately by a superadmin, never inferred from an empty
-- operator_scopes query.
ALTER TABLE operators ADD COLUMN global_access BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN operators.global_access IS
    'Explicit OPERATOR_GLOBAL entitlement (audit P0.1). A non-superadmin operator with zero operator_scopes rows and global_access=false has zero tenant/device access. Must be set explicitly by a superadmin -- never inferred from an empty operator_scopes result.';

-- One-time grandfather, not a runtime inference: every existing non-superadmin
-- operator that currently has no scope rows has had unrestricted access up to
-- now under the old default. Recording that as an explicit, reviewable grant
-- preserves current production access instead of silently locking every
-- existing deployment out the moment this ships. A superadmin can revoke it
-- per operator afterward exactly like any other global grant.
UPDATE operators
SET global_access = true
WHERE role <> 'superadmin'
  AND id NOT IN (SELECT DISTINCT operator_id FROM operator_scopes);
