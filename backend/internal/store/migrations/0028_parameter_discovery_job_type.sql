-- 0027 added the PARAMETER_DISCOVERY job type in Go but missed widening
-- jobs_type_check to allow it — caught live (jobs_type_check violation on
-- the very first BOOTSTRAP-triggered discovery job).
ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST', 'FIRMWARE_DOWNLOAD',
        'DIAGNOSTICS_PING', 'DIAGNOSTICS_TRACEROUTE', 'ADD_OBJECT', 'DELETE_OBJECT', 'REBOOT', 'FACTORY_RESET',
        'SCHEDULE_INFORM', 'SET_PARAMETER_ATTRIBUTES', 'GET_PARAMETER_ATTRIBUTES', 'UPLOAD', 'PARAMETER_DISCOVERY'));
