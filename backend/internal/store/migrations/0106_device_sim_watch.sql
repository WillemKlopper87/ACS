-- SIM change watch: one row per cellular CPE, holding the verdict on its
-- current SIM. Only masked hints (the last digits) and the network (PLMN)
-- are stored: never a full IMSI, ICCID or MSISDN. The full values stay in the
-- parameter cache and behind the audited superadmin reveal.
--   ok      the SIM is the one we know (or an operator approved it)
--   notice  a different SIM on a network the plan allows; awaits approval
--   alert   a different SIM on a network the plan does not allow
CREATE TABLE IF NOT EXISTS device_sim_watch (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('ok', 'notice', 'alert')),
    network TEXT NOT NULL DEFAULT '' CHECK (char_length(network) <= 6),
    iccid_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(iccid_hint) <= 64),
    imsi_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(imsi_hint) <= 64),
    previous_iccid_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(previous_iccid_hint) <= 64),
    previous_imsi_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(previous_imsi_hint) <= 64),
    detected_at TIMESTAMPTZ NULL,
    last_checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at TIMESTAMPTZ NULL,
    approved_by TEXT NULL,
    approval_reason TEXT NULL
);
CREATE INDEX IF NOT EXISTS device_sim_watch_open_idx ON device_sim_watch (state) WHERE state <> 'ok';
