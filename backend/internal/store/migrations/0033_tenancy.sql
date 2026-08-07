-- Admin-platform backlog: multi-tenancy. Shape confirmed with the user
-- 2026-08-06: single-owner hierarchy (region -> customer -> device) for
-- ISP/customer organization, with "project" as a separate cross-cutting
-- tag a device can carry several of (many-to-many, not part of the
-- ownership hierarchy). Operator accounts are scoped by assignment (see
-- operator_scopes) — no scope rows means unrestricted (backward
-- compatible default for existing operators), one or more rows means
-- restricted to just those regions/customers.
CREATE TABLE regions (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE customers (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    region_id UUID REFERENCES regions(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX customers_region_id_idx ON customers(region_id);

CREATE TABLE projects (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE devices ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX devices_customer_id_idx ON devices(customer_id);

CREATE TABLE device_projects (
    device_id UUID NOT NULL REFERENCES devices(id),
    project_id UUID NOT NULL REFERENCES projects(id),
    PRIMARY KEY (device_id, project_id)
);

-- scope_type/scope_id: ('region', regions.id) or ('customer', customers.id)
-- — a region scope implicitly covers every customer under it (resolved at
-- query time, not denormalized here) so assigning a whole region doesn't
-- require enumerating its customers.
CREATE TABLE operator_scopes (
    operator_id UUID NOT NULL REFERENCES operators(id),
    scope_type TEXT NOT NULL CHECK (scope_type IN ('region', 'customer')),
    scope_id UUID NOT NULL,
    PRIMARY KEY (operator_id, scope_type, scope_id)
);
