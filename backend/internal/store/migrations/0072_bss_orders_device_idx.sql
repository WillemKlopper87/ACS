-- 0072: BSS/OSS reconciliation (sub-project C, drift detection) needs to
-- find the most recent orders for one device efficiently -- previously
-- bss_orders had an index on account_id only, so "every order for device
-- X" was a full table scan.
CREATE INDEX bss_orders_device_idx ON bss_orders (device_id, created_at DESC) WHERE device_id IS NOT NULL;
