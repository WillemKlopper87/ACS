-- Audit P0.4: the SSH console bridge used ssh.InsecureIgnoreHostKey(),
-- leaving operator sessions open to machine-in-the-middle interception.
-- Host keys are now pinned per device: recorded on first successful
-- connect (trust-on-first-use), required to match on every connect
-- after. host_key is the OpenSSH authorized_keys wire encoding
-- (base64) of the device's public host key. Deleting a row is the
-- explicit re-enrollment path after a legitimate key change (e.g.
-- firmware reset), and key changes are audit-logged by the bridge.
CREATE TABLE device_ssh_host_keys (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    host_key TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
