-- A device's identity is oui_serial — "001349+NR5103+JOBDEMO00001" — which
-- is unique and stable but unusable in conversation: nobody reads it out on
-- a call or recognises it in a list. Every screen that showed a device was
-- therefore showing an identifier rather than a name.
--
-- `label` is the operator-chosen name for the unit: "Mrs Dlamini, 14 Oak
-- Ave", "Sandton POP rack 4". It is deliberately separate from `location`
-- (0035), which describes where the thing is rather than what it is called,
-- and from `tags`, which classify many devices at once rather than naming
-- one.
--
-- Nullable, with no uniqueness constraint: two subscribers may legitimately
-- have the same name, and most devices will have no label until someone
-- gives them one. oui_serial remains the identity the ACS matches on.
ALTER TABLE devices ADD COLUMN label TEXT;

-- Operators search the fleet by name, so make that search indexable rather
-- than a sequential scan once the fleet is large.
CREATE INDEX devices_label_idx ON devices (lower(label)) WHERE label IS NOT NULL;
