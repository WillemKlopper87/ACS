package resourceinventory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"acs/internal/devices"
	"acs/internal/tmf/common"
)

func TestProjectDeviceResource(t *testing.T) {
	label := "Living-room gateway"
	location := "Johannesburg lab"
	lat, lon := -26.2041, 28.0473
	lastInform := time.Date(2026, 9, 14, 18, 30, 0, 0, time.UTC)
	updated := lastInform.Add(time.Minute)

	d := devices.Device{
		ID:            "dev/one",
		OUISerial:     "AABBCC-ABC123",
		Manufacturer:  "Example Networks",
		OUI:           "AABBCC",
		ProductClass:  "Gateway-X",
		SerialNumber:  "ABC123",
		DataModelRoot: devices.DataModelRootDevice2,
		OnlineStatus:  "ONLINE",
		LastInformAt:  &lastInform,
		LastUpdatedAt: updated,
		Tags:          []string{"pilot", "5g"},
		Label:         &label,
		Location:      &location,
		Latitude:      &lat,
		Longitude:     &lon,
		// These values are intentionally populated to prove that the TMF
		// resource projection never exports management/authentication data.
		ConnectionRequestURL: func() *string { v := "http://10.0.0.5:7547/secret"; return &v }(),
		CWMPAuthMode:         "DIGEST",
	}

	got, err := NewProjector("https://acs.example.net/").ProjectWithEnrichment(d, Enrichment{
		ManagementProtocols: []string{"CWMP", "USP", "USP"},
		AssignmentRoles:     []string{"gateway"},
		SoftwareVersion:     "1.2.3",
		HardwareVersion:     "rev-b",
	})
	if err != nil {
		t.Fatalf("ProjectWithEnrichment() error = %v", err)
	}
	if got.ID != d.ID {
		t.Fatalf("ID = %q, want %q", got.ID, d.ID)
	}
	if got.Href != "https://acs.example.net/tmf-api/resourceInventoryManagement/v5/resource/dev%2Fone" {
		t.Fatalf("Href = %q", got.Href)
	}
	if got.Name != label {
		t.Fatalf("Name = %q, want %q", got.Name, label)
	}
	if got.OperationalState != "enabled" {
		t.Fatalf("OperationalState = %q, want enabled", got.OperationalState)
	}
	if got.AvailabilityStatus != "online" || got.LifecycleState != "installed" {
		t.Fatalf("unexpected TMF lifecycle state: %#v", got)
	}
	if got.Type != "PhysicalResource" || got.BaseType != "Resource" {
		t.Fatalf("unexpected TMF type metadata: %#v", got)
	}
	if got.SerialNumber != "ABC123" || got.ModelNumber != "Gateway-X" {
		t.Fatalf("unexpected physical identifiers: %#v", got)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal resource: %v", err)
	}
	body := string(encoded)
	for _, secretOrInternal := range []string{"10.0.0.5:7547", "DIGEST", "connection_request", "cwmp_auth"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(secretOrInternal)) {
			t.Fatalf("projection leaked %q: %s", secretOrInternal, body)
		}
	}
	for _, expected := range []string{
		"acsOuiSerial", "serialNumber", "location", "lastInformAt",
		"StringCharacteristic", "NumberCharacteristic", "StringArrayCharacteristic",
		"managementProtocols", "CWMP", "USP", "assignmentRole", "gateway",
		"softwareVersion", "1.2.3", "hardwareVersion", "rev-b",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("projection missing %q: %s", expected, body)
		}
	}
	if strings.Count(body, `"USP"`) != 1 {
		t.Fatalf("duplicate management protocol was not removed: %s", body)
	}
}

func TestProjectMultipleAssignmentRolesRemainTruthful(t *testing.T) {
	d := devices.Device{ID: "device-id", LastUpdatedAt: time.Unix(0, 0).UTC()}
	got, err := NewProjector("https://acs.example.net").ProjectWithEnrichment(d, Enrichment{
		AssignmentRoles: []string{"gateway", "extender", "gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(got)
	if !strings.Contains(string(body), `"assignmentRoles"`) || !strings.Contains(string(body), `"extender"`) {
		t.Fatalf("multi-account assignment roles were collapsed: %s", body)
	}
}

func TestProjectResourceNameFallback(t *testing.T) {
	d := devices.Device{
		ID:            "device-id",
		Manufacturer:  "Vendor",
		ProductClass:  "Model",
		SerialNumber:  "SN1",
		LastUpdatedAt: time.Unix(0, 0).UTC(),
	}
	got, err := NewProjector("https://acs.example.net").Project(d)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if got.Name != "Vendor Model SN1" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.AvailabilityStatus != "" {
		t.Fatalf("unknown availability was invented: %q", got.AvailabilityStatus)
	}
}

func TestOperationalState(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"ONLINE", "enabled"},
		{"offline", "disabled"},
		{"UNREACHABLE", "disabled"},
		{"", ""},
	} {
		if got := operationalState(tc.in); got != tc.want {
			t.Errorf("operationalState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProjectRejectsInvalidBaseURL(t *testing.T) {
	_, err := NewProjector("relative-only").Project(devices.Device{ID: "device-id"})
	if err == nil {
		t.Fatal("Project() error = nil, want invalid base URL error")
	}
}

func TestProjectMeshRelationships(t *testing.T) {
	updated := time.Unix(0, 0).UTC()
	projector := NewProjector("https://acs.example.net")

	satellite, err := projector.ProjectWithEnrichment(devices.Device{ID: "beacon", LastUpdatedAt: updated}, Enrichment{GatewayID: "fastmile"})
	if err != nil {
		t.Fatal(err)
	}
	if len(satellite.ResourceRelationship) != 1 {
		t.Fatalf("satellite relationships = %+v, want one", satellite.ResourceRelationship)
	}
	rel := satellite.ResourceRelationship[0]
	if rel.RelationshipType != RelationshipMeshSatelliteOf || rel.Type != "ResourceRelationship" ||
		rel.Resource.ID != "fastmile" || rel.Resource.ReferredType != "PhysicalResource" || rel.Resource.Type != "ResourceRef" ||
		rel.Resource.Href != "https://acs.example.net/tmf-api/resourceInventoryManagement/v5/resource/fastmile" {
		t.Fatalf("satellite relationship = %+v", rel)
	}

	gateway, err := projector.ProjectWithEnrichment(devices.Device{ID: "fastmile", LastUpdatedAt: updated},
		Enrichment{SatelliteIDs: []string{"beacon-b", "beacon-a", "beacon-b", "", "fastmile"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range gateway.ResourceRelationship {
		if r.RelationshipType != RelationshipHasMeshSatellite {
			t.Fatalf("gateway relationship type = %q", r.RelationshipType)
		}
		got = append(got, r.Resource.ID)
	}
	if strings.Join(got, ",") != "beacon-a,beacon-b" {
		t.Fatalf("gateway satellites = %v, want sorted, deduplicated, no self/blank", got)
	}

	body, _ := json.Marshal(ProjectFields(gateway, mustFields(t, "resourceRelationship")))
	if !strings.Contains(string(body), `"resourceRelationship"`) || !strings.Contains(string(body), `"relationshipType":"hasMeshSatellite"`) {
		t.Fatalf("fields=resourceRelationship projection = %s", body)
	}

	standalone, _ := projector.Project(devices.Device{ID: "solo", LastUpdatedAt: updated})
	if encoded, _ := json.Marshal(standalone); strings.Contains(string(encoded), "resourceRelationship") {
		t.Fatalf("unlinked device should omit resourceRelationship: %s", encoded)
	}
}

func mustFields(t *testing.T, raw string) common.Fields {
	t.Helper()
	fields, err := common.ParseFields(raw, AllowedFields...)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}
