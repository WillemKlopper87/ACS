-- 0055: USP subscription desired-state and per-device event stream.
--
-- usp_subscriptions is desired state only (design doc S7.1) -- there is no
-- requirement anywhere in this plan to preserve a subscription's own
-- history, only whether it currently should exist on the device. id is the
-- UUID the controller itself generates and writes verbatim into the
-- agent's Device.LocalAgent.Subscription.{i}.ID; USP gives the controller
-- full control over that ID (unlike, say, an agent-assigned endpoint id),
-- so there is no separate wire-id column to correlate against -- id *is*
-- the wire id.
--
-- reference_list is TEXT[], not a comma-joined string, so SQL queries and
-- tests can inspect individual paths directly; joining into USP's
-- comma-separated wire format is Task 4's job at encode time, not this
-- table's.
CREATE TABLE usp_subscriptions (
    id              UUID PRIMARY KEY,
    device_id       UUID NOT NULL REFERENCES devices(id),
    notif_type      TEXT NOT NULL CHECK (notif_type IN ('ValueChange','ObjectCreation','ObjectDeletion','OperationComplete','Event')),
    reference_list  TEXT[] NOT NULL,
    persistent      BOOLEAN NOT NULL DEFAULT false,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usp_subscriptions_device_idx ON usp_subscriptions (device_id);

-- device_events is the per-device event stream nothing in this codebase
-- has today: ObjectCreation, ObjectDeletion and Event notifications all
-- land here, distinguished by event_name (a fixed literal
-- "ObjectCreation"/"ObjectDeletion" for those two, or the Notify's own
-- EventName for Event) with obj_path/params carrying whatever detail each
-- kind provides (UniqueKeys for creation, the Params map for Event).
-- Populating those columns correctly is Task 5's job; this migration only
-- needs to support the shape.
--
-- UNIQUE (device_id, msg_id) is the idempotency mechanism the Decisions
-- section specifies: USP Notify delivery is at-least-once, so an agent
-- can (and will) redeliver the same msg_id after a dropped ack. This
-- constraint is a per-delivery-attempt dedup keyed on the transport-level
-- msg_id -- it is NOT a per-fact dedup. Two distinct events that happen to
-- carry the same obj_path/event_name/params are two separate rows if they
-- arrived with different msg_ids; only an exact msg_id replay collapses to
-- one row. A future reader must not repurpose this constraint to suppress
-- "duplicate-looking" events.
CREATE TABLE device_events (
    id           BIGSERIAL PRIMARY KEY,
    device_id    UUID NOT NULL REFERENCES devices(id),
    msg_id       TEXT NOT NULL,
    obj_path     TEXT NOT NULL,
    event_name   TEXT NOT NULL,
    params       JSONB NOT NULL DEFAULT '{}',
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (device_id, msg_id)
);

CREATE INDEX device_events_device_idx ON device_events (device_id, recorded_at DESC);
