-- Admin-platform backlog: customizable per-user fleet dashboards. One row
-- per operator, widgets as an ordered JSONB array of {id, enabled} —
-- intentionally freeform (not one column per widget) so adding a new
-- widget type later never needs a migration, only a new id the frontend
-- recognizes.
CREATE TABLE dashboard_layouts (
    operator_id UUID PRIMARY KEY REFERENCES operators(id),
    widgets JSONB NOT NULL DEFAULT '[]',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
