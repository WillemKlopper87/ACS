-- Build plan §5.4 firm-up: webhooks, described there as "new
-- infrastructure, not a wrapper around what exists" — an outbox pattern,
-- same shape as the jobs queue itself. Owned by cmd/bssadapter (not
-- cmd/acs): a subscription is inherently BSS-facing (keyed by account_id,
-- delivered over HTTP to a BSS-owned URL), matching this codebase's
-- existing "BSS-facing concerns stay in bssadapter, ACS core stays
-- unaware of BSS structures" boundary (build plan §5.1).
CREATE TABLE webhook_subscriptions (
    id UUID PRIMARY KEY,
    account_id TEXT,              -- NULL = fleet-wide subscription
    target_url TEXT NOT NULL,
    secret TEXT NOT NULL,         -- HMAC signing secret for X-Webhook-Signature
    event_types TEXT[] NOT NULL,  -- e.g. {'JOB_COMPLETED'}
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY,
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DELIVERED', 'FAILED')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_pending_idx ON webhook_deliveries (status, created_at) WHERE status = 'PENDING';

-- Lets the delivery poller find bss_orders whose underlying job has gone
-- terminal but hasn't yet produced a JOB_COMPLETED delivery, without
-- re-notifying on every poll tick.
ALTER TABLE bss_orders ADD COLUMN notified_at TIMESTAMPTZ;
