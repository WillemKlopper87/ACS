package adapters

// TR143Capability describes whether a device advertised the Broadband Forum
// throughput-diagnostic objects. Presence is intentionally derived from live
// discovery; a static catalog alone never authorizes a test against a URL.
type TR143Capability struct {
	DownloadPath string `json:"download_path,omitempty"`
	UploadPath   string `json:"upload_path,omitempty"`
}

// ResolveTR143Capability finds the standard TR-181 diagnostics objects in a
// discovered tree. TR-098 aliases are not guessed here: the caller must add a
// separately qualified mapping if a legacy CPE is known to support them.
func ResolveTR143Capability(names map[string]bool) TR143Capability {
	var capability TR143Capability
	for path := range names {
		if path == "Device.IP.Diagnostics.DownloadDiagnostics." || path == "Device.IP.Diagnostics.DownloadDiagnostics" {
			capability.DownloadPath = "Device.IP.Diagnostics.DownloadDiagnostics."
		}
		if path == "Device.IP.Diagnostics.UploadDiagnostics." || path == "Device.IP.Diagnostics.UploadDiagnostics" {
			capability.UploadPath = "Device.IP.Diagnostics.UploadDiagnostics."
		}
	}
	return capability
}

func (c TR143Capability) DownloadSupported() bool { return c.DownloadPath != "" }
func (c TR143Capability) UploadSupported() bool   { return c.UploadPath != "" }
