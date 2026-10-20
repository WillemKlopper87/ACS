-- Keep CPE->ACS Digest credential graduation single-flight per device.
-- Bootstrap already reuses an existing PENDING credential, but the operator
-- rotation endpoint is also able to create CWMP_DIGEST rows. This partial
-- unique index closes the concurrent/repeated-request race at the database
-- boundary while still allowing historical REVOKED/GRACE versions.
CREATE UNIQUE INDEX IF NOT EXISTS device_credentials_one_pending_cwmp_digest_idx
    ON device_credentials (device_id)
    WHERE credential_type = 'CWMP_DIGEST' AND status = 'PENDING';
