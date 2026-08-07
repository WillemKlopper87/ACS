package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"acs/internal/auth"
	"acs/internal/cwmp"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestFullProbeSequence acts as a minimal mock CPE (design doc v3 §15.3),
// driving one full Phase 0 handshake against the real HTTP handler:
// Inform -> InformResponse -> empty poll -> GetRPCMethods ->
// GetParameterNames(Device.) -> GetParameterNames(InternetGatewayDevice.)
// -> session closed with a compatibility-matrix record written.
func TestFullProbeSequence(t *testing.T) {
	resultsPath := filepath.Join(t.TempDir(), "results.jsonl")
	resultsFile, err := os.OpenFile(resultsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open results file: %v", err)
	}
	defer resultsFile.Close()

	h := &handler{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		auth:    auth.DigestAuthenticator{}, // disabled: no credentials configured
		store:   cwmp.NewSessionStore(),
		results: resultsFile,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cwmp", h.handleCWMP)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := server.Client()
	var cookie *http.Cookie

	post := func(body []byte) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/cwmp", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		return resp
	}

	// 1. Inform -> InformResponse, and the session cookie is issued.
	resp := post(readFixture(t, "inform_bootstrap.xml"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Inform: status = %d, want 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "acs_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("expected acs_session cookie to be set after Inform")
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "InformResponse") {
		t.Fatalf("expected InformResponse body, got: %s", body)
	}

	// 2. Empty poll -> ACS dispatches GetRPCMethods.
	resp = post(nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "GetRPCMethods") {
		t.Fatalf("expected GetRPCMethods request, got: %s", body)
	}

	// 3. GetRPCMethodsResponse -> ACS dispatches GetParameterNames(Device.).
	resp = post(readFixture(t, "get_rpc_methods_response.xml"))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<ParameterPath>Device.</ParameterPath>") {
		t.Fatalf("expected GetParameterNames(Device.) request, got: %s", body)
	}

	// 4. GetParameterNamesResponse -> ACS dispatches GetParameterNames(InternetGatewayDevice.).
	resp = post(readFixture(t, "get_parameter_names_response.xml"))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<ParameterPath>InternetGatewayDevice.</ParameterPath>") {
		t.Fatalf("expected GetParameterNames(InternetGatewayDevice.) request, got: %s", body)
	}

	// 5. Final GetParameterNamesResponse -> probe sequence complete, empty response.
	resp = post(readFixture(t, "get_parameter_names_response.xml"))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) != 0 {
		t.Fatalf("expected empty response closing the session, got: %s", body)
	}

	// The compatibility-matrix record should now be on disk.
	resultsFile.Close()
	f, err := os.Open(resultsPath)
	if err != nil {
		t.Fatalf("open results file for reading: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("expected at least one line in results file")
	}
	var rec probeRecord
	if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal probe record: %v", err)
	}
	if rec.Manufacturer != "Zyxel" || rec.SerialNumber != "S230Q12345678" {
		t.Errorf("probeRecord identity = %+v, want Zyxel/S230Q12345678", rec)
	}
	if !rec.Device2Supported {
		t.Error("expected Device2Supported=true in recorded probe result")
	}
	if len(rec.RPCMethods) == 0 {
		t.Error("expected RPCMethods to be recorded")
	}
}
