-- Durable claim lease for TMF641 item execution. RUNNING items whose lease
-- expires can be reclaimed after an adapter crash.
ALTER TABLE service_order_items
    DROP CONSTRAINT service_order_items_status_check;

ALTER TABLE service_order_items
    ADD CONSTRAINT service_order_items_status_check
        CHECK (status IN ('PENDING', 'RUNNING', 'DISPATCHED', 'COMPLETED', 'FAILED', 'SKIPPED'));

ALTER TABLE service_order_items
    ADD COLUMN claimed_at TIMESTAMPTZ;

CREATE INDEX service_order_items_recovery_idx
    ON service_order_items (claimed_at, order_id, seq)
    WHERE status IN ('PENDING', 'RUNNING');
