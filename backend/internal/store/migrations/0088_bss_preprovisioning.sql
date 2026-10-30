-- BSS pre-provisioning (docs/ONBOARDING.md): a TMF641 add or a mapping
-- request for a CPE the ACS has not seen yet becomes a PLANNED onboarding.
-- The order item waits (AWAITING_DEVICE) and later items wait behind it;
-- cmd/bssadapter writes the account mapping PENDING_ACTIVE on first contact,
-- ACTIVE at IN_SERVICE, and completes the item.

ALTER TABLE service_order_items
    DROP CONSTRAINT service_order_items_status_check;
ALTER TABLE service_order_items
    ADD CONSTRAINT service_order_items_status_check
        CHECK (status IN ('PENDING', 'RUNNING', 'AWAITING_DEVICE', 'DISPATCHED', 'COMPLETED', 'FAILED', 'SKIPPED'));
ALTER TABLE service_order_items
    ADD COLUMN onboarding_id UUID REFERENCES onboardings(id);
CREATE INDEX service_order_items_awaiting_idx ON service_order_items (onboarding_id) WHERE status = 'AWAITING_DEVICE';

-- What the BSS asked for, kept for the mapping written on first contact:
-- its role vocabulary is wider than the onboarding's (stb, ata), and its
-- plan code is kept even when no ACS service plan has that code.
ALTER TABLE onboardings
    ADD COLUMN bss_role TEXT,
    ADD COLUMN bss_service_plan TEXT,
    ADD COLUMN bss_mapping_id UUID REFERENCES account_device_mappings(id);
CREATE INDEX onboardings_bss_idx ON onboardings (state) WHERE source = 'bss';
