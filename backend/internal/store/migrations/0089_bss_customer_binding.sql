-- Tenant binding for BSS integrations (I1): an OAuth client can be bound to
-- one ACS customer (an ISP), and every BSS account belongs to at most one
-- customer. A bound client acts only on accounts its customer owns, and
-- claims a new account on its first write to it. Account ids stay globally
-- unique (the NaaS platform mints them); ownership, not per-customer
-- uniqueness, is what keeps one ISP out of the accounts of another.

ALTER TABLE bss_oauth_clients
    ADD COLUMN customer_id UUID REFERENCES customers(id);

-- A scoped client is bound to a customer, listed accounts or the fleet;
-- a customer binding never combines with fleet-wide access.
ALTER TABLE bss_oauth_clients
    DROP CONSTRAINT bss_oauth_clients_account_policy_ck,
    ADD CONSTRAINT bss_oauth_clients_account_policy_ck CHECK (
        global_access OR customer_id IS NOT NULL OR cardinality(account_ids) > 0 OR cardinality(scopes) = 0
    ),
    ADD CONSTRAINT bss_oauth_clients_customer_mode_ck CHECK (
        NOT (global_access AND customer_id IS NOT NULL)
    );

CREATE TABLE bss_accounts (
    account_id TEXT PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES customers(id),
    claimed_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bss_accounts_customer_idx ON bss_accounts (customer_id);
