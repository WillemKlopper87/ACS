-- Admin-platform backlog: Excel reporting wants a "location" column, and
-- nothing in this ACS tracks physical device location today (TR-069 has
-- no standard parameter for it — it's operator-entered metadata, same
-- category as tags). Free text rather than structured address/GPS
-- fields: real-world entries range from "Rack 4, POP-West" to full
-- addresses to lat/long, and forcing a schema on that now would be a
-- guess about a field nothing yet populates.
ALTER TABLE devices ADD COLUMN location TEXT;
