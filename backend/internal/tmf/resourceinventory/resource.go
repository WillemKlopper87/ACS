package resourceinventory

// CollectionPath is the TMF639 v5 Resource collection exposed by the
// northbound adapter. The internal Resource model stays version-neutral; this
// path belongs to the HTTP adapter boundary and is centralized here so hrefs
// and route registration cannot drift independently.
const CollectionPath = "tmf-api/resourceInventoryManagement/v5/resource"

// Characteristic is the small TMF characteristic shape needed by the ACS
// inventory projection. Value deliberately remains any: device facts include
// strings, booleans, numbers and string arrays.
type Characteristic struct {
	Name      string `json:"name"`
	ValueType string `json:"valueType,omitempty"`
	Value     any    `json:"value"`
}

// Resource is the read model projected from an ACS-managed device. It is not a
// second resource database: the devices table remains authoritative and this
// shape is rebuilt on every read.
type Resource struct {
	ID                     string           `json:"id"`
	Href                   string           `json:"href"`
	Name                   string           `json:"name,omitempty"`
	Description            string           `json:"description,omitempty"`
	Category               string           `json:"category,omitempty"`
	OperationalState       string           `json:"operationalState,omitempty"`
	LastUpdate             string           `json:"lastUpdate,omitempty"`
	ResourceCharacteristic []Characteristic `json:"resourceCharacteristic,omitempty"`
	BaseType               string           `json:"@baseType,omitempty"`
	Type                   string           `json:"@type"`
}

// AllowedFields is the projection allow-list accepted by ?fields=. Identity
// fields are still emitted by the HTTP adapter even when a projection is used,
// matching the common TMF expectation that id/href remain addressable.
var AllowedFields = []string{
	"id",
	"href",
	"name",
	"description",
	"category",
	"operationalState",
	"lastUpdate",
	"resourceCharacteristic",
	"@baseType",
	"@type",
}
