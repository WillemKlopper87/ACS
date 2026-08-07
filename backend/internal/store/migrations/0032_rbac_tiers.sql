-- Admin-platform backlog: RBAC tier expansion. The user asked for
-- superadmin/Manager/NOC/Read-only(ISP) with superadmin-configurable
-- per-role capabilities. Scope decision (confirmed with the user,
-- 2026-08-06): rather than replace all 72 routes' simple rank check with
-- individually configurable permissions (large diff, high regression risk
-- on a security-critical surface), the existing rank gate stays for
-- routine read/write routes — admin(now superadmin) still ranks above
-- manager above noc above readonly, so nothing that worked before breaks
-- — and role_permissions below adds a genuinely configurable layer only
-- for the ~13 highest-stakes capabilities (see internal/operators
-- permission catalog).
ALTER TABLE operators DROP CONSTRAINT operators_role_check;
UPDATE operators SET role = 'superadmin' WHERE role = 'admin';
UPDATE operators SET role = 'noc' WHERE role = 'operator';
ALTER TABLE operators ADD CONSTRAINT operators_role_check CHECK (role IN ('superadmin', 'manager', 'noc', 'readonly'));

-- superadmin is never stored here — it always has every permission,
-- enforced in code (internal/operators.HasPermission), not editable even
-- by another superadmin, so there's no path to a fleet with zero
-- permission-configurable superadmin.
CREATE TABLE role_permissions (
    role TEXT NOT NULL CHECK (role IN ('manager', 'noc', 'readonly')),
    permission TEXT NOT NULL,
    granted BOOLEAN NOT NULL,
    PRIMARY KEY (role, permission)
);

ALTER TABLE operators ADD COLUMN email TEXT;

CREATE TABLE password_reset_tokens (
    token TEXT PRIMARY KEY,
    operator_id UUID NOT NULL REFERENCES operators(id),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
