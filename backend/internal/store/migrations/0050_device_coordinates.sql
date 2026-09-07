-- 0035_device_location added a free-text `location` column and explicitly
-- declined to impose structure on it, because nothing populated it and
-- real entries range from "Rack 4, POP-West" to a full street address.
-- That reasoning still holds for the human label, so `location` is
-- unchanged and stays the thing an operator reads.
--
-- What it could not do is answer "which offline device do I drive to
-- first, and where is it" on a map. That needs machine-readable
-- coordinates, so they get their own columns rather than a parsing
-- convention layered onto free text. Both are nullable: a device with no
-- survey data is the normal case, and the locator dashboard simply does
-- not plot it.
ALTER TABLE devices ADD COLUMN latitude DOUBLE PRECISION;
ALTER TABLE devices ADD COLUMN longitude DOUBLE PRECISION;

-- Reject impossible fixes at the database rather than trusting every
-- future caller. NULL passes both checks, which is what allows a device
-- to carry a location string but no coordinates.
ALTER TABLE devices ADD CONSTRAINT devices_latitude_range
    CHECK (latitude IS NULL OR (latitude >= -90 AND latitude <= 90));
ALTER TABLE devices ADD CONSTRAINT devices_longitude_range
    CHECK (longitude IS NULL OR (longitude >= -180 AND longitude <= 180));

-- The locator dashboard's hot query is "every device in scope that has a
-- fix", so the partial index carries only the rows it can actually plot.
CREATE INDEX devices_coordinates_idx ON devices (latitude, longitude)
    WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
