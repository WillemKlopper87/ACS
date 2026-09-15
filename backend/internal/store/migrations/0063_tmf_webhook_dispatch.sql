-- Fan-out ledger for TMF event hub delivery. This makes event publication
-- restart-safe and prevents one subscription receiving duplicate deliveries.
CREATE TABLE tmf_webhook_dispatches (
    event_id UUID NOT NULL REFERENCES tmf_events(id) ON DELETE CASCADE,
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, subscription_id)
);
