-- 0052: device assignment becomes addressable by role and temporal.
--
-- Spec: docs/superpowers/specs/2026-09-09-fleet-data-model-design.md §5.
--
-- Order dispatch used to pick "the most recently updated ACTIVE mapping",
-- so on a multi-device account an order silently targeted whichever device
-- was touched last. Role makes "the gateway for account X" resolve to one
-- row or none. unassigned_at makes assignment temporal: history is the
-- rows with it set, current state the rows with it NULL, one table, one
-- source of truth.
--
-- Backfill: every existing row becomes role=gateway (so single-device
-- accounts keep working with no BSS change) and assigned_at=now(). That
-- assigned_at is a LOWER BOUND on a fact never previously recorded, not
-- real history -- the operator console labels backfilled rows.
--
-- If any account already has two active rows, the role-unique index
-- creation below FAILS. That is deliberate: an operator resolves the
-- duplicate, rather than this migration guessing which device to demote.

ALTER TABLE account_device_mappings
    ADD COLUMN role TEXT NOT NULL DEFAULT 'gateway'
        CHECK (role IN ('gateway', 'ont', 'extender', 'stb', 'ata', 'other')),
    ADD COLUMN assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN unassigned_at TIMESTAMPTZ,
    ADD COLUMN unassign_reason TEXT
        CHECK (unassign_reason IN ('rma', 'upgrade', 'return', 'moved', 'corrected'));

-- Postgres's auto-generated name for the unnamed inline
-- UNIQUE (account_id, device_id) in 0007; no later migration altered it.
ALTER TABLE account_device_mappings
    DROP CONSTRAINT account_device_mappings_account_id_device_id_key;

-- A device is assigned to an account at most once *currently*; it may be
-- assigned, released and reassigned over time.
CREATE UNIQUE INDEX account_device_mappings_active_idx
    ON account_device_mappings (account_id, device_id)
    WHERE unassigned_at IS NULL;

-- One active device per role per account. This is the constraint that
-- makes addressing safe, and it forces close-before-open on swap: a
-- replacement gateway cannot be inserted while the old one is active.
CREATE UNIQUE INDEX account_device_mappings_active_role_idx
    ON account_device_mappings (account_id, role)
    WHERE unassigned_at IS NULL;

CREATE INDEX account_device_mappings_device_active_idx
    ON account_device_mappings (device_id)
    WHERE unassigned_at IS NULL;
