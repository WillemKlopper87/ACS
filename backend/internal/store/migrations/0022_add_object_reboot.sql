-- Critical-feature backlog: AddObject/DeleteObject/Reboot/FactoryReset —
-- the biggest protocol-completeness gap against an off-the-shelf ACS.
-- Every prior write path (SET_PARAMETER) could only edit parameters that
-- already existed on the device; provisioning a genuinely new table row
-- (a second WLAN SSID, a port-forward rule) was impossible without these.
ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST', 'FIRMWARE_DOWNLOAD',
        'DIAGNOSTICS_PING', 'DIAGNOSTICS_TRACEROUTE', 'ADD_OBJECT', 'DELETE_OBJECT', 'REBOOT', 'FACTORY_RESET'));

-- Nowhere else for ADD_OBJECT's CPE-assigned InstanceNumber to live —
-- SET_PARAMETER's "result" lands in device_parameter_cache via a
-- confirmation read, but a newly created object instance has no prior
-- cache entry to confirm against.
ALTER TABLE jobs ADD COLUMN result_detail JSONB;
