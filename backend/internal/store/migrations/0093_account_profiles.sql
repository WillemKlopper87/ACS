-- Subscriber profile per BSS account, for operator lookup and readable
-- device names (internal/subscribers). Holds the minimum needed to find
-- a subscriber: a display name and a VIP flag. The BSS is the source of
-- truth for the name: a name it sent (name_source 'bss') is not editable
-- by operators, who may only fill in one the BSS has not sent. Either may
-- set VIP. Personal information (POPIA): name and address lookups are
-- audited (SubscriberLookup).
CREATE TABLE account_profiles (
    account_id   TEXT PRIMARY KEY,
    display_name TEXT,
    name_source  TEXT CHECK (name_source IN ('bss', 'operator')),
    vip          BOOLEAN NOT NULL DEFAULT false,
    vip_source   TEXT CHECK (vip_source IN ('bss', 'operator')),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by   TEXT,
    CHECK ((display_name IS NULL) = (name_source IS NULL))
);

CREATE INDEX account_profiles_vip_idx ON account_profiles (account_id) WHERE vip;
