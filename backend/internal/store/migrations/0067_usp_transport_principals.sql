-- 0067: cryptographically authenticated USP transport principals.
--
-- usp_agents (0053) is deliberately live connection state. It cannot be the
-- authority that decides whether a new connection is allowed to claim an
-- EndpointID, because its endpoint_id is learned from that connection. This
-- table is the pre-provisioned trust anchor instead: an operator binds a
-- device to the SHA-256 fingerprint of its client certificate, the EndpointID
-- that certificate is allowed to speak as, and the one MQTT response topic
-- the agent is allowed to use.
--
-- Production uspc requires a client certificate verified by
-- ACS_USP_CLIENT_CA_CERT, then resolves the verified leaf certificate's
-- fingerprint through this table before registering either WebSocket or MQTT
-- connections. CIDR filtering remains defence in depth only.
CREATE TABLE usp_transport_principals (
    device_id              UUID PRIMARY KEY REFERENCES devices(id),
    endpoint_id            TEXT NOT NULL UNIQUE,
    client_cert_sha256     TEXT NOT NULL UNIQUE,
    mqtt_topic             TEXT NOT NULL UNIQUE,
    enabled                BOOLEAN NOT NULL DEFAULT true,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT usp_transport_principals_endpoint_not_blank
        CHECK (btrim(endpoint_id) <> ''),
    CONSTRAINT usp_transport_principals_cert_sha256_hex
        CHECK (client_cert_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usp_transport_principals_mqtt_topic_not_blank
        CHECK (btrim(mqtt_topic) <> ''),
    CONSTRAINT usp_transport_principals_mqtt_topic_no_wildcards
        CHECK (position('#' in mqtt_topic) = 0 AND position('+' in mqtt_topic) = 0)
);

CREATE INDEX usp_transport_principals_enabled_idx
    ON usp_transport_principals (enabled);

COMMENT ON TABLE usp_transport_principals IS
    'Pre-provisioned USP transport identity authority: verified client-certificate fingerprint -> device, EndpointID, and unique MQTT response topic. Separate from live usp_agents state by design.';
