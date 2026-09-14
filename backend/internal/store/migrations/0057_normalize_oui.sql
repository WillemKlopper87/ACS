-- Backfill for cwmp.DeviceID.NormalizeOUI: NaturalKey now canonicalizes
-- OUI to uppercase with no separators before building oui_serial, so a
-- device whose Inform once arrived with, say, "aa:bb:cc" is looked up
-- under "AABBCC" from now on. Without this backfill, any row already
-- stored under a non-canonical OUI form would stop matching its own
-- future Informs -- looking like a brand-new device rather than the same
-- one, silently creating a duplicate devices row.
--
-- OUI is never itself escaped in oui_serial (NaturalKey only escapes a
-- literal "+"/"\" within a field, and a real OUI never legitimately
-- contains either), so the OUI segment is reliably everything before the
-- first "+" -- SPLIT_PART(oui_serial, '+', 1). The transform is
-- idempotent: a row whose OUI is already canonical is rewritten to the
-- same value.
UPDATE devices
SET oui_serial = REPLACE(REPLACE(UPPER(SPLIT_PART(oui_serial, '+', 1)), ':', ''), '-', '')
                 || SUBSTRING(oui_serial FROM POSITION('+' IN oui_serial))
WHERE POSITION('+' IN oui_serial) > 0;

UPDATE devices
SET oui = REPLACE(REPLACE(UPPER(oui), ':', ''), '-', '')
WHERE oui IS NOT NULL;

-- account_device_mappings.oui_serial is a denormalized copy tied to
-- devices by device_id (not a live FK on the text value, migration
-- 0007) -- resync it from the now-normalized devices row rather than
-- re-deriving the same transform a second time, so the two can never
-- drift apart from a transform bug in one but not the other.
UPDATE account_device_mappings m
SET oui_serial = d.oui_serial
FROM devices d
WHERE m.device_id = d.id
  AND m.oui_serial <> d.oui_serial;
