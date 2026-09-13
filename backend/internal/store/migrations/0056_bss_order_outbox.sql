-- 0056: outbox durability for BSS order dispatch (sub-project C-1,
-- design docs/superpowers/specs/2026-09-13-bss-improvements-design.md
-- S3). bss_orders previously only recorded an order AFTER dispatch
-- already succeeded (command_key was NOT NULL from creation) -- a crash
-- or DB error between dispatch succeeding and that write being made lost
-- the fact dispatch had happened, and a retried external_order_id would
-- silently dispatch a second job. The row is now written BEFORE dispatch
-- is attempted (status = 'PENDING_DISPATCH'), so command_key must become
-- nullable: it is genuinely unknown until dispatch actually succeeds.
--
-- device_id and parameters are captured at insert time, not just the
-- action name, so a reconciler retry (cmd/bssadapter/order_reconciler.go)
-- replays exactly what the original attempt would have sent -- it must
-- never re-resolve the account's active device (which could have changed,
-- e.g. a device swap) or re-run bss.Translate.
--
-- Every pre-existing row already has a command_key (the column was
-- NOT NULL until this migration), so status's DEFAULT 'DISPATCHED' below
-- correctly backfills every one of them without a separate UPDATE.
ALTER TABLE bss_orders
    ALTER COLUMN command_key DROP NOT NULL,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'DISPATCHED'
        CHECK (status IN ('PENDING_DISPATCH', 'DISPATCHED', 'DEAD_LETTERED')),
    ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_error TEXT,
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN device_id UUID REFERENCES devices(id),
    ADD COLUMN parameters JSONB;

-- Partial index: the reconciler's ClaimDuePendingOrders query (originally
-- DuePendingOrders, a plain SELECT; made an atomic claim by the final
-- review's fix for findings 1 and 3) filters on status =
-- 'PENDING_DISPATCH' every poll tick; this is the same pattern
-- webhook_deliveries' own partial PENDING index already uses.
CREATE INDEX bss_orders_pending_idx ON bss_orders (status) WHERE status = 'PENDING_DISPATCH';
