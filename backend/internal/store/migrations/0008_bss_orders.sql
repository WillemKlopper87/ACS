-- Phase 8b (build plan §5.3). The reference internal_bss_adapter.go draft
-- has no dedupe on external_order_id — a BSS retry on timeout would
-- create a second job. This table makes order submission idempotent:
-- on a duplicate external_order_id, return the existing job instead of
-- creating a new one.
--
-- command_key (not job_id/a jobs FK): bssadapter only ever talks to the
-- internal ACS REST API over HTTP (design §5.1) and never sees a job's
-- internal UUID — only the public command_key the API returns. A DB-level
-- FK to jobs(id) would leak cmd/api's internal identity across that
-- process boundary; command_key is the contract both sides actually share.
CREATE TABLE bss_orders (
    external_order_id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL,
    action TEXT NOT NULL,
    command_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bss_orders_account_idx ON bss_orders (account_id);
