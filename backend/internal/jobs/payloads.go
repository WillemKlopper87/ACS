// Per-type job payload shapes (split out of job.go, audit P3.1).
package jobs

// SetParameterPayload is the payload shape for a SET_PARAMETER job,
// mirroring the REST PUT /devices/{id}/parameters body (design doc v3
// §8.3).
type SetParameterPayload struct {
	Parameters []ParameterWrite `json:"parameters"`
}

type ParameterWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

// GetParameterPayload is the payload shape for a GET_PARAMETER job.
type GetParameterPayload struct {
	Paths []string `json:"paths"`
}

// ConnectionRequestPayload is the payload shape for a CONNECTION_REQUEST
// job, mirroring the REST POST /devices/{id}/connection-request body
// (design doc v3 §8.4).
type ConnectionRequestPayload struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

// FirmwareDownloadPayload is the payload shape for a FIRMWARE_DOWNLOAD
// job (design doc v3 §9.2's Download arguments, resolved from a
// firmware_images row at job-creation time rather than re-resolved at
// dispatch time — the job should download the exact image it was created
// against even if a newer one gets uploaded in between).
type FirmwareDownloadPayload struct {
	FirmwareImageID string `json:"firmware_image_id"`
	FileType        string `json:"file_type"`
	URL             string `json:"url"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	FileSize        int64  `json:"file_size"`
	TargetFilename  string `json:"target_filename"`
	DelaySeconds    int    `json:"delay_seconds"`
}

// DiagnosticsPingPayload is the payload shape for a DIAGNOSTICS_PING job
// (design doc v3 §10.1's Device.IP.Diagnostics.IPPing input parameters).
// It stays attached to the job across the whole trigger->poll->poll...
// cycle since Requeue never touches payload — only the first (attempt 1)
// dispatch reads it.
type DiagnosticsPingPayload struct {
	Host                string `json:"host"`
	NumberOfRepetitions int    `json:"number_of_repetitions"`
	Timeout             int    `json:"timeout"`
	DataBlockSize       int    `json:"data_block_size"`
	DSCP                int    `json:"dscp"`
	// Prefix is the diagnostic's data-model root object (e.g.
	// "Device.IP.Diagnostics.IPPing." or
	// "InternetGatewayDevice.IPPingDiagnostics."), resolved once from the
	// target device's discovered data_model_root at job-creation time —
	// the same "resolve once at creation, not re-resolved at dispatch"
	// precedent FirmwareDownloadPayload already established, so a poll
	// dispatched minutes later still reads the same subtree the trigger
	// wrote to even if discovery updates the device's root in between.
	// Empty means "resolve TR-181 for backward compatibility" (any job
	// queued before this field existed) — cmd/acs falls back accordingly.
	Prefix string `json:"prefix,omitempty"`
}

// DiagnosticsTraceroutePayload is the payload shape for a
// DIAGNOSTICS_TRACEROUTE job (design doc v3 §10.1's sibling diagnostic —
// same TR-181 trigger/poll shape as IPPing, different parameter subtree:
// Device.IP.Diagnostics.TraceRoute.*). Build plan §4 Phase 5's explicitly
// deferred item, built here as "the identical pattern" it was always
// described as.
type DiagnosticsTraceroutePayload struct {
	Host          string `json:"host"`
	NumberOfTries int    `json:"number_of_tries"`
	Timeout       int    `json:"timeout"`
	DataBlockSize int    `json:"data_block_size"`
	DSCP          int    `json:"dscp"`
	MaxHopCount   int    `json:"max_hop_count"`
	// Prefix — see DiagnosticsPingPayload.Prefix's doc comment; same
	// resolve-once-at-creation convention, same backward-compat default.
	Prefix string `json:"prefix,omitempty"`
}

// AddObjectPayload is the payload shape for an ADD_OBJECT job.
// ObjectPath is the parent path ending in "." (e.g. "Device.WiFi.SSID.")
// — the CPE picks the new instance number and returns it, recorded on
// job completion (build plan "critical feature backlog": AddObject/
// DeleteObject were the biggest protocol-completeness gap against an
// off-the-shelf ACS — every prior write path could only edit parameters
// that already existed on the device).
type AddObjectPayload struct {
	ObjectPath string `json:"object_path"`
}

// DeleteObjectPayload is the payload shape for a DELETE_OBJECT job.
// ObjectPath is the full path to the instance being removed, ending in
// "." (e.g. "Device.WiFi.SSID.3.").
type DeleteObjectPayload struct {
	ObjectPath string `json:"object_path"`
}

// RebootPayload and FactoryResetPayload carry no fields — TR-069 gives
// neither RPC any arguments beyond (for Reboot) the CommandKey every job
// already has.
type RebootPayload struct{}

type FactoryResetPayload struct{}

// ScheduleInformPayload is the payload shape for a SCHEDULE_INFORM job.
type ScheduleInformPayload struct {
	DelaySeconds int `json:"delay_seconds"`
}

// AttributeWrite mirrors cwmp.AttributeWrite for JSON payload storage
// (this package doesn't import internal/cwmp, matching the existing
// duplication convention every other payload type here already follows).
type AttributeWrite struct {
	Name         string `json:"name"`
	Notification int    `json:"notification"`
}

// SetParameterAttributesPayload is the payload shape for a
// SET_PARAMETER_ATTRIBUTES job.
type SetParameterAttributesPayload struct {
	Attributes []AttributeWrite `json:"attributes"`
}

// GetParameterAttributesPayload is the payload shape for a
// GET_PARAMETER_ATTRIBUTES job.
type GetParameterAttributesPayload struct {
	Paths []string `json:"paths"`
}

// UploadPayload is the payload shape for an UPLOAD job. FileType follows
// TR-069 §A.3.2.7's enumeration (e.g. "1 Vendor Configuration File", "2
// Vendor Log File"). URL points at cmd/api's upload-receipt endpoint —
// resolved at job-creation time from a fresh per-job token, the same
// "resolve once, not re-resolved at dispatch" reasoning
// FirmwareDownloadPayload already uses for its firmware image URL.
type UploadPayload struct {
	FileType     string `json:"file_type"`
	URL          string `json:"url"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	DelaySeconds int    `json:"delay_seconds"`
}

// ParameterDiscoveryPayload is the payload shape for a PARAMETER_DISCOVERY
// job — a full-tree GetParameterNames(Root, NextLevel=false) sent
// automatically the first time a device connects (BOOTSTRAP), or on demand
// for an already-onboarded device. Root is tried first; if the CPE returns
// a fault or an empty list (the standard signal that a path doesn't exist
// under that root), completeJob chains one fallback job at FallbackRoot
// rather than guessing — IsFallback marks that second attempt so the chain
// stops after one retry instead of looping.
type ParameterDiscoveryPayload struct {
	Root         string `json:"root"`
	FallbackRoot string `json:"fallback_root,omitempty"`
	IsFallback   bool   `json:"is_fallback,omitempty"`
}
