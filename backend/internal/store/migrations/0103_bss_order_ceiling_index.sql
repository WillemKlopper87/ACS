-- Counting one customer's destructive orders of the last hour (the adapter's
-- ceiling on REBOOT, FACTORY_RESET and SUSPEND) must not scan the table.
CREATE INDEX IF NOT EXISTS bss_orders_ceiling_idx
    ON bss_orders (order_scope, action, created_at)
    WHERE action IN ('REBOOT', 'FACTORY_RESET', 'SUSPEND');
