package main

import (
	"net/http"

	"acs/internal/config"
)

type systemInfoResponse struct {
	// IntegrationMode is "naas" or "standalone": whether this ACS is run
	// through the ISP NaaS platform or on its own.
	IntegrationMode string `json:"integration_mode"`
	// DeploymentProfile is "lab" or "production".
	DeploymentProfile string `json:"deployment_profile"`
}

// systemInfo tells the console how this ACS is being run, so it can show
// what applies: no BSS / NaaS screen on an ACS that runs on its own.
// Nothing here is secret; it needs a signed-in operator all the same.
func (h *handler) systemInfo(w http.ResponseWriter, r *http.Request) {
	mode, profile := h.integrationMode, h.deploymentProfile
	if mode == "" {
		mode = config.ModeNaaS
	}
	if profile == "" {
		profile = config.ProfileLab
	}
	writeJSON(w, http.StatusOK, systemInfoResponse{IntegrationMode: mode, DeploymentProfile: profile})
}
