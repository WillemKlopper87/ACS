-- 0053: USP agent identity.
--
-- Sub-project B-3a: the one identity a device has is OUI+ProductClass+
-- SerialNumber (sub-project A, devices.oui_serial). usp_agents links that
-- same devices row to a live USP endpoint id and MTP -- it never
-- introduces a second identity key. One row per device, overwritten on
-- reconnect: this is live connection state, not history.
--
-- management_protocols records which management protocols (CWMP, USP, ...)
-- a device answers to, independent of whether it currently has a live
-- usp_agents row -- a device can support USP without being connected right
-- now.

ALTER TABLE devices
  ADD COLUMN management_protocols TEXT[] NOT NULL DEFAULT '{}';

-- mtp_kind's two values are mtp.KindWebSocket / mtp.KindMQTT's exact
-- string values (internal/usp/mtp/transport.go) -- the reference agent
-- (obuspa) writes the same strings into Device.LocalAgent.MTP.{i}.Protocol,
-- so a value read from the data model compares directly, no translation.
--
-- endpoint_id is UNIQUE on its own (not composite with device_id) because
-- at most one live agent uses a given endpoint id at a time -- mtp.Registry
-- already enforces this per-process; this constraint enforces it durably.
--
-- No customer_id column: tenancy is resolved through devices, not
-- duplicated here.
--
-- No ON DELETE CASCADE on device_id: device deletion is out of scope for
-- this plan and CWMP has no precedent for it either -- leave the FK to
-- fail loudly if that ever changes.
CREATE TABLE usp_agents (
    device_id                     UUID PRIMARY KEY REFERENCES devices(id),
    endpoint_id                   TEXT NOT NULL UNIQUE,
    mtp_kind                      TEXT NOT NULL CHECK (mtp_kind IN ('WebSocket', 'MQTT')),
    connected                     BOOLEAN NOT NULL DEFAULT true,
    last_connected_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    supported_protocol_versions   TEXT[] NOT NULL DEFAULT '{}',
    controller_role               TEXT
);

CREATE INDEX usp_agents_connected_idx ON usp_agents (connected);
