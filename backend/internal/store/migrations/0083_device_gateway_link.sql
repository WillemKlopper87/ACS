-- Mesh satellite -> gateway link (field test 2026-10-30). A Nokia WiFi
-- Beacon 3.1 behind a FastMile 5G Gateway 2 reports the upstream gateway's
-- identity through TR-111 Device.GatewayInfo.{ManufacturerOUI,ProductClass,
-- SerialNumber}; nothing stored it, so the console showed two unrelated
-- devices. gateway_oui_serial is the natural key the satellite last reported
-- (kept even when that gateway is not registered yet, so it can be adopted
-- when it first informs); gateway_device_id is the resolved row.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS gateway_oui_serial TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS gateway_device_id UUID
    REFERENCES devices(id) ON DELETE SET NULL;
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_gateway_not_self;
ALTER TABLE devices ADD CONSTRAINT devices_gateway_not_self
    CHECK (gateway_device_id IS NULL OR gateway_device_id <> id);
CREATE INDEX IF NOT EXISTS devices_gateway_device_id_idx
    ON devices (gateway_device_id) WHERE gateway_device_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS devices_gateway_oui_serial_idx
    ON devices (gateway_oui_serial) WHERE gateway_oui_serial IS NOT NULL;
