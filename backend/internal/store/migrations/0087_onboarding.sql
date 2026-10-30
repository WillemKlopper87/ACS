-- Business onboarding (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md §4, gap
-- analysis 2026-10-30): who a CPE belongs to, where it is installed, what
-- service it delivers, and its progress from a sale (PLANNED, before the
-- device has ever connected) to IN_SERVICE.
--
-- Subscribers stay in the BSS: an onboarding keeps only the BSS account
-- reference. Sites are created by the ACS or mirrored from the BSS
-- (external_ref).

CREATE TABLE sites (
    id UUID PRIMARY KEY,
    customer_id UUID REFERENCES customers(id),
    external_ref TEXT,
    name TEXT NOT NULL,
    street TEXT,
    suburb TEXT,
    city TEXT,
    postal_code TEXT,
    country TEXT,
    latitude DOUBLE PRECISION,
    longitude DOUBLE PRECISION,
    region_id UUID REFERENCES regions(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((latitude IS NULL) = (longitude IS NULL))
);
CREATE UNIQUE INDEX sites_external_ref_idx ON sites (COALESCE(customer_id::text, ''), external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX sites_customer_idx ON sites (customer_id);

-- What a sale means for the CPE: templates to apply, the cellular
-- profile (APN/DNN; no APN password in this version) and the networks a
-- SIM may belong to (MCC+MNC, e.g. '65506').
CREATE TABLE service_plans (
    id UUID PRIMARY KEY,
    customer_id UUID REFERENCES customers(id),
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    template_ids UUID[] NOT NULL DEFAULT '{}',
    cellular_profile JSONB NOT NULL DEFAULT '{}',
    allowed_plmns TEXT[] NOT NULL DEFAULT '{}',
    require_discovery BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX service_plans_code_idx ON service_plans (COALESCE(customer_id::text, ''), code);

CREATE TABLE onboardings (
    id UUID PRIMARY KEY,
    customer_id UUID REFERENCES customers(id),
    device_id UUID REFERENCES devices(id) ON DELETE SET NULL,
    expected_oui TEXT,
    expected_serial TEXT NOT NULL,
    expected_imei TEXT,
    expected_iccid TEXT,
    expected_imsi TEXT,
    account_ref TEXT,
    site_id UUID REFERENCES sites(id),
    service_plan_id UUID REFERENCES service_plans(id),
    role TEXT NOT NULL DEFAULT 'gateway' CHECK (role IN ('gateway', 'extender', 'ont', 'other')),
    commercial_model TEXT NOT NULL DEFAULT 'retail' CHECK (commercial_model IN ('retail', 'wholesale', 'enterprise')),
    source TEXT NOT NULL CHECK (source IN ('console', 'bss', 'import')),
    external_ref TEXT,
    state TEXT NOT NULL DEFAULT 'PLANNED' CHECK (state IN
        ('PLANNED', 'CONTACTED', 'SECURED', 'DISCOVERED', 'CONFIGURING', 'IN_SERVICE', 'ON_HOLD', 'FAILED', 'CANCELLED')),
    state_reason TEXT,
    config_jobs TEXT[] NOT NULL DEFAULT '{}',
    requested_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    contacted_at TIMESTAMPTZ,
    in_service_at TIMESTAMPTZ
);
-- One open onboarding per serial: a sale cannot be planned twice for the
-- same CPE, and the CPE's first Inform has exactly one record to match.
CREATE UNIQUE INDEX onboardings_open_serial_idx ON onboardings (expected_serial)
    WHERE state NOT IN ('IN_SERVICE', 'CANCELLED');
CREATE INDEX onboardings_device_idx ON onboardings (device_id);
CREATE INDEX onboardings_state_idx ON onboardings (state, updated_at);

ALTER TABLE devices ADD COLUMN site_id UUID REFERENCES sites(id);
