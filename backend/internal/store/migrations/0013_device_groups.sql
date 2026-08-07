-- Phase 7 (build plan §4 Phase 7 / design doc v3 Phase 7: "Device groups,
-- Tags"). Two independent, deliberately lightweight mechanisms for
-- targeting more than one device:
--   - device_groups / device_group_members: explicit, named, curated
--     sets an operator builds up over time (e.g. "Branch Office Routers")
--     and can target directly from Fleet Control's bulk actions via
--     group_id, instead of re-selecting devices every time.
--   - devices.tags: freeform per-device labels for filtering/display —
--     no dedicated join table, since membership here isn't curated the
--     same way (a device can pick up a tag from many uncoordinated
--     sources) and Postgres's array/GIN support is a fine fit for
--     "devices with tag X" queries without one more table.
CREATE TABLE device_groups (
    id UUID PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE device_group_members (
    group_id UUID NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, device_id)
);

CREATE INDEX device_group_members_device_id_idx ON device_group_members(device_id);

ALTER TABLE devices ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX devices_tags_idx ON devices USING GIN (tags);
