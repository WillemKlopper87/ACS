-- TMF641 Service Ordering Management.
-- The parent records the northbound order; dispatching work continues to
-- use bss_orders so the existing outbox/retry/reconciliation guarantees stay
-- intact.
CREATE TABLE service_orders (
    id UUID PRIMARY KEY,
    external_id TEXT NOT NULL UNIQUE,
    account_id TEXT NOT NULL,
    cancelled_at TIMESTAMPTZ,
    raw_request JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX service_orders_account_idx ON service_orders (account_id);

CREATE TABLE service_order_items (
    id UUID PRIMARY KEY,
    order_id UUID NOT NULL REFERENCES service_orders(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK (seq >= 0),
    action TEXT NOT NULL CHECK (action IN ('add', 'modify', 'delete', 'noChange')),
    role TEXT,
    mapping_id UUID REFERENCES account_device_mappings(id),
    external_order_id TEXT REFERENCES bss_orders(external_order_id),
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'DISPATCHED', 'COMPLETED', 'FAILED', 'SKIPPED')),
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    UNIQUE (order_id, seq)
);

CREATE INDEX service_order_items_order_idx ON service_order_items (order_id, seq);
