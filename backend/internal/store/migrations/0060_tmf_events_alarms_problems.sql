-- TMF642 alarms, TMF688 events, and TMF656 service problems.
-- These records are append-oriented operational evidence. The source IDs
-- make ingestion idempotent; service/device/account identifiers retain the
-- tenancy and correlation boundary without duplicating device state.
CREATE TABLE tmf_alarms (
    id UUID PRIMARY KEY,
    source_key TEXT NOT NULL UNIQUE,
    account_id TEXT,
    device_id UUID REFERENCES devices(id),
    service_id UUID REFERENCES account_device_mappings(id),
    alarm_type TEXT NOT NULL,
    perceived_severity TEXT NOT NULL CHECK (perceived_severity IN ('critical','major','minor','warning','indeterminate')),
    state TEXT NOT NULL DEFAULT 'raised' CHECK (state IN ('raised','acknowledged','cleared')),
    probable_cause TEXT,
    specific_problem TEXT,
    raised_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cleared_at TIMESTAMPTZ,
    last_event_id UUID,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX tmf_alarms_account_state_idx ON tmf_alarms (account_id, state);
CREATE INDEX tmf_alarms_device_state_idx ON tmf_alarms (device_id, state);

CREATE TABLE tmf_events (
    id UUID PRIMARY KEY,
    source_key TEXT NOT NULL UNIQUE,
    account_id TEXT,
    device_id UUID REFERENCES devices(id),
    service_id UUID REFERENCES account_device_mappings(id),
    event_type TEXT NOT NULL,
    event_time TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX tmf_events_account_time_idx ON tmf_events (account_id, event_time DESC);

CREATE TABLE tmf_service_problems (
    id UUID PRIMARY KEY,
    external_id TEXT UNIQUE,
    account_id TEXT NOT NULL,
    service_id UUID REFERENCES account_device_mappings(id),
    status TEXT NOT NULL DEFAULT 'acknowledged'
        CHECK (status IN ('acknowledged','inProgress','resolved','closed')),
    priority TEXT,
    problem_type TEXT NOT NULL,
    description TEXT NOT NULL,
    related_alarm_id UUID REFERENCES tmf_alarms(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    resolution TEXT
);
CREATE INDEX tmf_service_problems_account_status_idx ON tmf_service_problems (account_id, status);
