-- Phase 8a (build plan §5.2). Durable replacement for the in-memory map
-- in the reference internal_bss_adapter.go draft. device_id is a real FK
-- into devices — the mapping repository must resolve and validate
-- oui_serial against an existing device before inserting, not trust
-- whatever the BSS caller sends (the draft skipped this check).
CREATE TABLE account_device_mappings (
    id UUID PRIMARY KEY,
    account_id TEXT NOT NULL,
    device_id UUID NOT NULL REFERENCES devices(id),
    oui_serial TEXT NOT NULL,
    service_plan TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('PENDING_ACTIVE', 'ACTIVE', 'SUSPENDED', 'TERMINATED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, device_id)
);

CREATE INDEX account_device_mappings_account_idx ON account_device_mappings (account_id);
CREATE INDEX account_device_mappings_device_idx ON account_device_mappings (device_id);
