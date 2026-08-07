package bss

import (
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

func TestNewACSClientDefaultsTimeout(t *testing.T) {
	client := NewACSClient("http://example.invalid", 0, "")
	if client.http.Timeout != defaultHTTPTimeout {
		t.Errorf("Timeout = %v, want default %v", client.http.Timeout, defaultHTTPTimeout)
	}
}
