package resourceinventory

// CollectionPath is the TMF639 v5 Resource collection exposed by the
// northbound adapter. The internal Resource model stays version-neutral; this
// path belongs to the HTTP adapter boundary and is centralized here so hrefs
// and route registration cannot drift independently.
const CollectionPath = "tmf-api/resourceInventoryManagement/v5/resource"

// Characteristic is the TMF v5 characteristic envelope used by the ACS
// projection. @type identifies the concrete strongly-typed characteristic
// family (for example StringCharacteristic or NumberCharacteristic).
type Characteristic struct {
	Name      string `json:"name"`
	ValueType string `json:"valueType,omitempty"`
	Value     any    `json:"value"`
	Type      string `json:"@type"`
}

// Mesh relationship types (TMF639 relationshipType is free text). A mesh
// satellite (e.g. Nokia WiFi Beacon 3.1) points at the gateway it reported via
// TR-111 Device.GatewayInfo; the gateway lists each satellite in turn.
const (
	RelationshipMeshSatelliteOf  = "isMeshSatelliteOf"
	RelationshipHasMeshSatellite = "hasMeshSatellite"
)

// ResourceRef is the TMF v5 reference to another resource in this inventory.
type ResourceRef struct {
	ID           string `json:"id"`
	Href         string `json:"href"`
	ReferredType string `json:"@referredType"`
	Type         string `json:"@type"`
}

// ResourceRelationship links this resource to another one.
type ResourceRelationship struct {
	RelationshipType string      `json:"relationshipType"`
	Resource         ResourceRef `json:"resource"`
	Type             string      `json:"@type"`
}

// Resource is the read model projected from an ACS-managed physical CPE. It is
// not a second resource database: the devices table remains authoritative and
// this shape is rebuilt on every read.
type Resource struct {
	ID                     string                 `json:"id"`
	Href                   string                 `json:"href"`
	Name                   string                 `json:"name,omitempty"`
	Description            string                 `json:"description,omitempty"`
	Category               string                 `json:"category,omitempty"`
	SerialNumber           string                 `json:"serialNumber,omitempty"`
	ModelNumber            string                 `json:"modelNumber,omitempty"`
	OperationalState       string                 `json:"operationalState,omitempty"`
	LifecycleState         string                 `json:"lifecycleState,omitempty"`
	AvailabilityStatus     string                 `json:"availabilityStatus,omitempty"`
	LastUpdate             string                 `json:"lastUpdate,omitempty"`
	ResourceCharacteristic []Characteristic       `json:"resourceCharacteristic,omitempty"`
	ResourceRelationship   []ResourceRelationship `json:"resourceRelationship,omitempty"`
	BaseType               string                 `json:"@baseType,omitempty"`
	Type                   string                 `json:"@type"`
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
	"serialNumber",
	"modelNumber",
	"operationalState",
	"lifecycleState",
	"availabilityStatus",
	"lastUpdate",
	"resourceCharacteristic",
	"resourceRelationship",
	"@baseType",
	"@type",
}
