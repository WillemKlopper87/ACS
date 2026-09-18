package bss

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestACSClientSetParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/devices/dev-1/parameters" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body setParametersRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if len(body.Parameters) != 1 || body.Parameters[0].Name != "Device.WiFi.SSID.1.SSID" {
			t.Errorf("unexpected parameters: %+v", body.Parameters)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(queueResponse{CommandKey: "setparam_test_0001", Status: "QUEUED"})
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	commandKey, err := client.SetParameters(t.Context(), "dev-1", []ParameterWrite{
		{Name: "Device.WiFi.SSID.1.SSID", Value: "TestSSID", Type: "string"},
	})
	if err != nil {
		t.Fatalf("SetParameters: %v", err)
	}
	if commandKey != "setparam_test_0001" {
		t.Errorf("commandKey = %q, want setparam_test_0001", commandKey)
	}
}

func TestACSClientSetParametersSendsServiceToken(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(queueResponse{CommandKey: "setparam_test_0002", Status: "QUEUED"})
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "internal-secret-abc")
	if _, err := client.SetParameters(t.Context(), "dev-1", []ParameterWrite{{Name: "x", Value: "y"}}); err != nil {
		t.Fatalf("SetParameters: %v", err)
	}
	if want := "Bearer internal-secret-abc"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestACSClientSetParametersUnreachable(t *testing.T) {
	client := NewACSClient("http://127.0.0.1:1", time.Millisecond*50, "")
	_, err := client.SetParameters(t.Context(), "dev-1", []ParameterWrite{{Name: "x", Value: "y"}})
	if !errors.Is(err, ErrACSUnreachable) {
		t.Errorf("err = %v, want ErrACSUnreachable", err)
	}
}

func TestACSClientGetJobStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobs/setparam_test_0001" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(JobStatus{CommandKey: "setparam_test_0001", Status: "SUCCESS"})
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	status, err := client.GetJobStatus(t.Context(), "setparam_test_0001")
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if status.Status != "SUCCESS" {
		t.Errorf("Status = %q, want SUCCESS", status.Status)
	}
}

func TestACSClientGetJobStatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	_, err := client.GetJobStatus(t.Context(), "unknown")
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("err = %v, want ErrJobNotFound", err)
	}
}

func TestACSClientGetDevice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/dev-1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(DeviceSummary{ID: "dev-1", DataModelRoot: "IGD1"})
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	dev, err := client.GetDevice(t.Context(), "dev-1")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if dev.DataModelRoot != "IGD1" {
		t.Errorf("DataModelRoot = %q, want IGD1", dev.DataModelRoot)
	}
}

func TestACSClientGetDeviceNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	_, err := client.GetDevice(t.Context(), "unknown")
	if !errors.Is(err, ErrDeviceLookupNotFound) {
		t.Errorf("err = %v, want ErrDeviceLookupNotFound", err)
	}
}

func TestNewACSClientDefaultsTimeout(t *testing.T) {
	client := NewACSClient("http://example.invalid", 0, "")
	if client.http.Timeout != defaultHTTPTimeout {
		t.Errorf("Timeout = %v, want default %v", client.http.Timeout, defaultHTTPTimeout)
	}
}

func TestACSClientGetParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/devices/dev-1/parameters" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("paths"); got != "Device.WiFi.SSID.1.SSID,Device.WiFi.AccessPoint.1.Security.KeyPassphrase" {
			t.Fatalf("paths query = %q, want the two requested paths comma-joined", got)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"parameters": map[string]any{
				"Device.WiFi.SSID.1.SSID": map[string]any{
					"value": "MyNetwork", "type": "string", "updated_at": "2026-09-13T10:00:00Z", "source": "GET_PARAMETER_VALUES",
				},
			},
		})
	}))
	defer server.Close()

	client := NewACSClient(server.URL, time.Second, "")
	got, err := client.GetParameters(context.Background(), "dev-1", []string{"Device.WiFi.SSID.1.SSID", "Device.WiFi.AccessPoint.1.Security.KeyPassphrase"})
	if err != nil {
		t.Fatalf("GetParameters: %v", err)
	}
	v, ok := got["Device.WiFi.SSID.1.SSID"]
	if !ok || v.Value != "MyNetwork" {
		t.Errorf("GetParameters result = %+v, want Device.WiFi.SSID.1.SSID = MyNetwork", got)
	}
}

func TestACSClientGetParametersUnreachable(t *testing.T) {
	client := NewACSClient("http://127.0.0.1:1", time.Millisecond*50, "")
	_, err := client.GetParameters(context.Background(), "dev-1", []string{"p"})
	if !errors.Is(err, ErrACSUnreachable) {
		t.Fatalf("GetParameters against an unreachable ACS = %v, want ErrACSUnreachable", err)
	}
}

func TestACSClientGetParameterNamesPreservesDiscoveryAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/devices/dev-1/parameter-names" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"names": map[string]bool{}, "discovered_at": nil})
	}))
	defer server.Close()

	names, err := NewACSClient(server.URL, time.Second, "").GetParameterNames(t.Context(), "dev-1")
	if err != nil {
		t.Fatalf("GetParameterNames: %v", err)
	}
	if names != nil {
		t.Errorf("names = %v, want nil for an undiscovered device", names)
	}
}

func TestACSClientGetParameterNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"names":         map[string]bool{"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase": true},
			"discovered_at": "2026-09-16T10:00:00Z",
		})
	}))
	defer server.Close()

	names, err := NewACSClient(server.URL, time.Second, "").GetParameterNames(t.Context(), "dev-1")
	if err != nil {
		t.Fatalf("GetParameterNames: %v", err)
	}
	if !names["InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase"] {
		t.Errorf("names = %v, want discovered writable path", names)
	}
}
