package resourceinventory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"acs/internal/devices"
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

	got, err := NewProjector("https://acs.example.net/").Project(d)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
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
	if got.OperationalState != "enable" {
		t.Fatalf("OperationalState = %q, want enable", got.OperationalState)
	}
	if got.Type != "CustomerPremisesEquipment" || got.BaseType != "Resource" {
		t.Fatalf("unexpected TMF type metadata: %#v", got)
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
	for _, expected := range []string{"acsOuiSerial", "serialNumber", "location", "lastInformAt"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("projection missing %q: %s", expected, body)
		}
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
}

func TestOperationalState(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"ONLINE", "enable"},
		{"offline", "disable"},
		{"UNREACHABLE", "disable"},
		{"", "unknown"},
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
