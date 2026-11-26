-- Remembers when a SimChanged event was last sent for a device and for which
-- verdict, so a CPE that flips its SIM identity on every reply cannot flood
-- subscribers, and two overlapping writes cannot publish the same change twice.
ALTER TABLE device_sim_watch
    ADD COLUMN IF NOT EXISTS last_event_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS last_event_state TEXT NULL;
