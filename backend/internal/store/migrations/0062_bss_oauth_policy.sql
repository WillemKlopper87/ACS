-- Bind BSS OAuth clients to explicit TMF permissions and tenant/account
-- entitlements. Existing clients pre-date this policy model, so preserve
-- their current effective access during migration, then make the defaults
-- fail closed for every client created afterwards.
ALTER TABLE bss_oauth_clients
    ADD COLUMN scopes TEXT[] NOT NULL DEFAULT ARRAY[
        'tmf:read',
        'tmf:write',
        'tmf:execute',
        'tmf:acknowledge'
    ]::TEXT[],
    ADD COLUMN account_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN global_access BOOLEAN NOT NULL DEFAULT TRUE;

-- New rows must opt into both scopes and fleet-wide access explicitly.
ALTER TABLE bss_oauth_clients
    ALTER COLUMN scopes SET DEFAULT ARRAY[]::TEXT[],
    ALTER COLUMN global_access SET DEFAULT FALSE;

ALTER TABLE bss_oauth_clients
    ADD CONSTRAINT bss_oauth_clients_account_policy_ck CHECK (
        global_access OR cardinality(account_ids) > 0 OR cardinality(scopes) = 0
    );
