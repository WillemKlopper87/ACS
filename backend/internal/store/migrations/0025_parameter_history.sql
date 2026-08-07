-- Nice-to-have feature backlog: parameter value history. The existing
-- device_parameter_cache is latest-value-only by design (v3 §7.7); this
-- is an insert-only companion table so a value's actual trend (RF signal
-- strength drifting, an uptime counter resetting) is visible, not just
-- its current reading. A row is only ever inserted when a value actually
-- changes (see internal/parameters.Repository.Upsert) — not on every
-- Inform that re-reports an unchanged value — so this doesn't grow
-- unbounded on a stable fleet.
CREATE TABLE parameter_history (
    id BIGSERIAL PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    name TEXT NOT NULL,
    value TEXT NOT NULL,
    type TEXT,
    source TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX parameter_history_device_name_idx ON parameter_history (device_id, name, recorded_at DESC);
