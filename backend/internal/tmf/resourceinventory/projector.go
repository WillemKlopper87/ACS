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
		{Name: "acsOuiSerial", ValueType: "string", Value: d.OUISerial},
		{Name: "manufacturer", ValueType: "string", Value: d.Manufacturer},
		{Name: "oui", ValueType: "string", Value: d.OUI},
		{Name: "productClass", ValueType: "string", Value: d.ProductClass},
		{Name: "serialNumber", ValueType: "string", Value: d.SerialNumber},
		{Name: "dataModelRoot", ValueType: "string", Value: d.DataModelRoot},
		{Name: "acsOnlineStatus", ValueType: "string", Value: d.OnlineStatus},
	}
	if len(d.Tags) > 0 {
		characteristics = append(characteristics, Characteristic{Name: "tags", ValueType: "string[]", Value: append([]string(nil), d.Tags...)})
	}
	if d.Location != nil && strings.TrimSpace(*d.Location) != "" {
		characteristics = append(characteristics, Characteristic{Name: "location", ValueType: "string", Value: *d.Location})
	}
	if d.Latitude != nil {
		characteristics = append(characteristics, Characteristic{Name: "latitude", ValueType: "number", Value: *d.Latitude})
	}
	if d.Longitude != nil {
		characteristics = append(characteristics, Characteristic{Name: "longitude", ValueType: "number", Value: *d.Longitude})
	}
	if d.LastInformAt != nil {
		characteristics = append(characteristics, Characteristic{Name: "lastInformAt", ValueType: "dateTime", Value: d.LastInformAt.UTC().Format(time.RFC3339)})
	}

	return Resource{
		ID:                     d.ID,
		Href:                   href,
		Name:                   resourceName(d),
		Description:            "ACS-managed customer premises equipment",
		Category:               "CustomerPremisesEquipment",
		OperationalState:       operationalState(d.OnlineStatus),
		LastUpdate:             d.LastUpdatedAt.UTC().Format(time.RFC3339),
		ResourceCharacteristic: characteristics,
		BaseType:               "Resource",
		Type:                   "CustomerPremisesEquipment",
	}, nil
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
		return "enable"
	case "OFFLINE", "UNREACHABLE":
		return "disable"
	default:
		return "unknown"
	}
}
