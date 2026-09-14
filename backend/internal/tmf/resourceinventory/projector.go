package resourceinventory

import (
	"fmt"
	"strings"
	"time"

	"acs/internal/devices"
	"acs/internal/tmf/common"
)

// Projector converts the existing ACS device inventory into the canonical
// resource read model. It intentionally has no database dependency so the
// source-of-truth boundary is explicit and easy to test.
type Projector struct {
	baseURL string
}

func NewProjector(baseURL string) *Projector {
	return &Projector{baseURL: strings.TrimSpace(baseURL)}
}

func (p *Projector) Project(d devices.Device) (Resource, error) {
	href, err := common.BuildHref(p.baseURL, CollectionPath, d.ID)
	if err != nil {
		return Resource{}, fmt.Errorf("build TMF639 resource href: %w", err)
	}

	characteristics := []Characteristic{
		stringCharacteristic("acsOuiSerial", d.OUISerial),
		stringCharacteristic("manufacturer", d.Manufacturer),
		stringCharacteristic("oui", d.OUI),
		stringCharacteristic("productClass", d.ProductClass),
		stringCharacteristic("dataModelRoot", d.DataModelRoot),
		stringCharacteristic("acsOnlineStatus", d.OnlineStatus),
	}
	if len(d.Tags) > 0 {
		characteristics = append(characteristics, Characteristic{
			Name: "tags", ValueType: "StringArray", Value: append([]string(nil), d.Tags...), Type: "StringArrayCharacteristic",
		})
	}
	if d.Location != nil && strings.TrimSpace(*d.Location) != "" {
		characteristics = append(characteristics, stringCharacteristic("location", *d.Location))
	}
	if d.Latitude != nil {
		characteristics = append(characteristics, numberCharacteristic("latitude", *d.Latitude))
	}
	if d.Longitude != nil {
		characteristics = append(characteristics, numberCharacteristic("longitude", *d.Longitude))
	}
	if d.LastInformAt != nil {
		// TMF639 v5's characteristic hierarchy has no dedicated date-time
		// characteristic; represent the RFC3339 value as a StringCharacteristic.
		characteristics = append(characteristics, stringCharacteristic("lastInformAt", d.LastInformAt.UTC().Format(time.RFC3339)))
	}

	resource := Resource{
		ID:                     d.ID,
		Href:                   href,
		Name:                   resourceName(d),
		Description:            "ACS-managed customer premises equipment",
		Category:               "CustomerPremisesEquipment",
		SerialNumber:           d.SerialNumber,
		ModelNumber:            d.ProductClass,
		OperationalState:       operationalState(d.OnlineStatus),
		LifecycleState:         "installed",
		LastUpdate:             d.LastUpdatedAt.UTC().Format(time.RFC3339),
		ResourceCharacteristic: characteristics,
		BaseType:               "Resource",
		Type:                   "PhysicalResource",
	}
	if strings.EqualFold(strings.TrimSpace(d.OnlineStatus), "ONLINE") {
		resource.AvailabilityStatus = "online"
	}
	return resource, nil
}

func stringCharacteristic(name, value string) Characteristic {
	return Characteristic{Name: name, ValueType: "String", Value: value, Type: "StringCharacteristic"}
}

func numberCharacteristic(name string, value float64) Characteristic {
	return Characteristic{Name: name, ValueType: "Number", Value: value, Type: "NumberCharacteristic"}
}

func resourceName(d devices.Device) string {
	if d.Label != nil && strings.TrimSpace(*d.Label) != "" {
		return strings.TrimSpace(*d.Label)
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(d.Manufacturer) != "" {
		parts = append(parts, strings.TrimSpace(d.Manufacturer))
	}
	if strings.TrimSpace(d.ProductClass) != "" {
		parts = append(parts, strings.TrimSpace(d.ProductClass))
	}
	if strings.TrimSpace(d.SerialNumber) != "" {
		parts = append(parts, strings.TrimSpace(d.SerialNumber))
	}
	if len(parts) == 0 {
		return d.ID
	}
	return strings.Join(parts, " ")
}

func operationalState(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ONLINE":
		return "enabled"
	case "OFFLINE", "UNREACHABLE":
		return "disabled"
	default:
		// TMF639 v5 only defines enabled/disabled. An unknown ACS state is
		// represented by omitting operationalState rather than inventing a
		// non-standard enum value.
		return ""
	}
}
