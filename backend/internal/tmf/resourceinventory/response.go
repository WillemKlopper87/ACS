package resourceinventory

import "acs/internal/tmf/common"

// ProjectFields converts a canonical Resource into the JSON object emitted by
// the v5 adapter. id and href are always retained so projected list results
// remain individually addressable; every other field follows ?fields=.
func ProjectFields(resource Resource, fields common.Fields) map[string]any {
	out := map[string]any{
		"id":   resource.ID,
		"href": resource.Href,
	}
	if fields.Includes("name") && resource.Name != "" {
		out["name"] = resource.Name
	}
	if fields.Includes("description") && resource.Description != "" {
		out["description"] = resource.Description
	}
	if fields.Includes("category") && resource.Category != "" {
		out["category"] = resource.Category
	}
	if fields.Includes("operationalState") && resource.OperationalState != "" {
		out["operationalState"] = resource.OperationalState
	}
	if fields.Includes("lastUpdate") && resource.LastUpdate != "" {
		out["lastUpdate"] = resource.LastUpdate
	}
	if fields.Includes("resourceCharacteristic") && len(resource.ResourceCharacteristic) > 0 {
		out["resourceCharacteristic"] = resource.ResourceCharacteristic
	}
	if fields.Includes("@baseType") && resource.BaseType != "" {
		out["@baseType"] = resource.BaseType
	}
	if fields.Includes("@type") && resource.Type != "" {
		out["@type"] = resource.Type
	}
	return out
}
