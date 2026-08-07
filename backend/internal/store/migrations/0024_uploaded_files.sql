-- Upload RPC (nice-to-have feature backlog): the CPE-to-ACS direction of
-- file transfer, mirroring firmware_images' shape for the opposite
-- direction. A row starts PENDING the moment an UPLOAD job is created
-- (its id is the token embedded in the receipt URL) and becomes RECEIVED
-- once the CPE's PUT has actually landed on disk.
CREATE TABLE uploaded_files (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    file_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'RECEIVED')),
    filename TEXT,
    file_size_bytes BIGINT,
    sha256 TEXT,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    received_at TIMESTAMPTZ
);
CREATE INDEX uploaded_files_device_idx ON uploaded_files (device_id, created_at DESC);
