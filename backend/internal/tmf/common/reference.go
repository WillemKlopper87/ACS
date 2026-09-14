package common

import "strings"

// EntityRef is the version-neutral identity of another ACS/TMF entity. Exact
// TMF relationship/reference DTOs are constructed by each HTTP adapter.
type EntityRef struct {
	ID   string
	Href string
	Name string
}

// ExternalReference ties an ACS-owned operational entity to an identifier
// owned by a northbound BSS/OSS or partner system.
type ExternalReference struct {
	ID    string
	Owner string
	Type  string
}

func (r EntityRef) Valid() bool {
	return strings.TrimSpace(r.ID) != "" && strings.TrimSpace(r.Href) != ""
}

func (r ExternalReference) Valid() bool {
	return strings.TrimSpace(r.ID) != "" && strings.TrimSpace(r.Owner) != ""
}
