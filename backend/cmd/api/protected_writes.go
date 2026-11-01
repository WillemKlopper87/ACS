package main

import (
	"net/http"

	"acs/internal/jobs"
	"acs/internal/parameters"
)

// refuseProtectedWrite answers 403 with the reason when names include a
// parameter that can cut a CPE off from the ACS (parameters.ProtectedReason),
// and reports whether it did. Job creation refuses these writes anyway;
// checking first gives the operator the reason up front, and keeps
// templates, policies and schedules that could never be applied from being
// saved at all.
func refuseProtectedWrite(w http.ResponseWriter, names ...string) bool {
	if err := parameters.CheckProtected(names...); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return true
	}
	return false
}

func writeNames(p jobs.SetParameterPayload) []string {
	names := make([]string, 0, len(p.Parameters))
	for _, w := range p.Parameters {
		names = append(names, w.Name)
	}
	return names
}
