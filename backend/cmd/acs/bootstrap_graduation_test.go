package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/credentials"
	"acs/internal/cwmp"
	"acs/internal/devices"
)

func bootstrapInformWithRoot(prefix string) string {
	param := `<ParameterList><ParameterValueStruct><Name>` + prefix + `.DeviceInfo.SoftwareVersion</Name><Value>1.0</Value></ParameterValueStruct></ParameterList>`
	return strings.Replace(bootstrapInformXML, `<ParameterList></ParameterList>`, param, 1)
}

func pendingBootstrapCredential() *credentials.Credential {
	return &credentials.Credential{
		ID:             "cred-bootstrap-1",
		DeviceID:       "device-bootstrap-1",
		CredentialType: credentials.TypeCWMPDigest,
		Version:        1,
		Username:       "cr-unique-device-1",
		Password:       "unique-device-password-0123456789",
		Status:         credentials.StatusPending,
		CommandKey:     "cwmp-bootstrap-cred-bootstrap-1",
	}
}

func bootstrapGraduationTestDeps(t *testing.T, root string) (bootstrapGraduationDeps, *credentials.Credential, *devices.Device) {
	t.Helper()
	authenticator := testBootstrapAuthenticator()
	cred := pendingBootstrapCredential()
	device := &devices.Device{
		ID:            cred.DeviceID,
		OUISerial:     "A1B2C3+N5368X+BOOTSTRAP-SERIAL-1",
		Manufacturer:  "Huawei",
		OUI:           "A1B2C3",
		ProductClass:  "N5368X",
		SerialNumber:  "BOOTSTRAP-SERIAL-1",
		DataModelRoot: root,
		CWMPAuthMode:  devices.AuthModeNone,
	}
	return bootstrapGraduationDeps{
		bootstrapAuth: authenticator,
		lookup: func(context.Context, string) (*devices.Device, error) {
			return nil, sql.ErrNoRows
		},
		ensureDevice: func(_ context.Context, id cwmp.DeviceID, inferredRoot string) (*devices.Device, error) {
			if id.NaturalKey() != device.OUISerial {
				t.Fatalf("bootstrap natural identity = %q, want %q", id.NaturalKey(), device.OUISerial)
			}
			if inferredRoot != root {
				t.Fatalf("inferred data-model root = %q, want %q", inferredRoot, root)
			}
			return device, nil
		},
		ensurePending: func(_ context.Context, deviceID string) (*credentials.Credential, error) {
			if deviceID != device.ID {
				t.Fatalf("pending credential device = %q, want %q", deviceID, device.ID)
			}
			return cred, nil
		},
		credentialByID: func(_ context.Context, id string) (*credentials.Credential, error) {
			if id != cred.ID {
				t.Fatalf("credential lookup id = %q, want %q", id, cred.ID)
			}
			return cred, nil
		},
		deviceByID: func(_ context.Context, id string) (*devices.Device, error) {
			if id != device.ID {
				t.Fatalf("device lookup id = %q, want %q", id, device.ID)
			}
			return device, nil
		},
	}, cred, device
}

func runBootstrapGraduationInform(t *testing.T, guard http.HandlerFunc, authn string) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, testBootstrapAuthenticator(), bootstrapInformWithRoot(authn)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap Inform status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "InformResponse") {
		t.Fatalf("bootstrap Inform response = %q, want InformResponse", recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == bootstrapCredentialCookieName {
			return cookie
		}
	}
	t.Fatal("bootstrap Inform did not emit graduation cookie")
	return nil
}

func TestBootstrapGraduationDevice2InstallsOnlyUniqueCredential(t *testing.T) {
	deps, cred, _ := bootstrapGraduationTestDeps(t, devices.DataModelRootDevice2)
	nextCalls := 0
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) { nextCalls++ }, deps)
	cookie := runBootstrapGraduationInform(t, guard, "Device")

	req := authenticatedBootstrapRequest(t, deps.bootstrapAuth, "")
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	guard(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap empty poll status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"SetParameterValues",
		"Device.ManagementServer.Username",
		"Device.ManagementServer.Password",
		cred.Username,
		cred.Password,
		cred.CommandKey,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("credential graduation RPC missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "ConnectionRequestUsername") || strings.Contains(body, "ConnectionRequestPassword") {
		t.Fatalf("graduation RPC attempted unrelated Connection Request credentials: %s", body)
	}
	if nextCalls != 0 {
		t.Fatalf("normal ACS handler called %d times during bootstrap graduation", nextCalls)
	}
}

func TestBootstrapGraduationIGD1UsesTR098ManagementServerPaths(t *testing.T) {
	deps, _, _ := bootstrapGraduationTestDeps(t, devices.DataModelRootIGD1)
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) {
		t.Fatal("normal handler entered during bootstrap graduation")
	}, deps)
	cookie := runBootstrapGraduationInform(t, guard, "InternetGatewayDevice")

	req := authenticatedBootstrapRequest(t, deps.bootstrapAuth, "")
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	guard(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("IGD1 empty poll status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "InternetGatewayDevice.ManagementServer.Username") ||
		!strings.Contains(body, "InternetGatewayDevice.ManagementServer.Password") {
		t.Fatalf("IGD1 graduation RPC used the wrong parameter tree: %s", body)
	}
}

func TestBootstrapGraduationUnknownRootRefusesCredentialRewrite(t *testing.T) {
	deps, _, device := bootstrapGraduationTestDeps(t, devices.DataModelRootUnknown)
	// The Inform deliberately carries no root-bearing parameter, matching a
	// device whose first contact does not reveal whether it is TR-181 or TR-098.
	deps.ensureDevice = func(context.Context, cwmp.DeviceID, string) (*devices.Device, error) {
		return device, nil
	}
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) {
		t.Fatal("normal handler entered during unknown-root bootstrap")
	}, deps)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, deps.bootstrapAuth, bootstrapInformXML))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unknown-root Inform status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("unknown-root bootstrap did not retain pending credential state for manual recovery")
	}

	req := authenticatedBootstrapRequest(t, deps.bootstrapAuth, "")
	req.AddCookie(cookies[0])
	recorder = httptest.NewRecorder()
	guard(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("unknown-root credential rewrite status = %d body=%q, want 409", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "SetParameterValues") {
		t.Fatalf("unknown-root bootstrap guessed a credential path: %s", recorder.Body.String())
	}
}

func TestBootstrapGraduationRequiresExplicitCookieState(t *testing.T) {
	deps, _, _ := bootstrapGraduationTestDeps(t, devices.DataModelRootDevice2)
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) {
		t.Fatal("normal handler entered for state-less bootstrap poll")
	}, deps)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, deps.bootstrapAuth, ""))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("bootstrap poll without state status = %d body=%q, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestBootstrapGraduationRejectsDeviceThatBecameEstablished(t *testing.T) {
	deps, _, device := bootstrapGraduationTestDeps(t, devices.DataModelRootDevice2)
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) {
		t.Fatal("normal handler entered during established-device bootstrap")
	}, deps)
	cookie := runBootstrapGraduationInform(t, guard, "Device")

	now := time.Now()
	device.LastInformAt = &now
	device.CWMPAuthMode = devices.AuthModeDigest
	req := authenticatedBootstrapRequest(t, deps.bootstrapAuth, "")
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	guard(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("post-establishment bootstrap status = %d body=%q, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestBootstrapGraduationSetParameterValuesResponseNeverActivates(t *testing.T) {
	deps, cred, _ := bootstrapGraduationTestDeps(t, devices.DataModelRootDevice2)
	guard := bootstrapCWMPGraduationGuardWithDeps(func(http.ResponseWriter, *http.Request) {
		t.Fatal("normal handler entered during bootstrap completion")
	}, deps)
	cookie := runBootstrapGraduationInform(t, guard, "Device")

	response := `<?xml version="1.0"?><soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-2"><soap-env:Body><cwmp:SetParameterValuesResponse><Status>0</Status></cwmp:SetParameterValuesResponse></soap-env:Body></soap-env:Envelope>`
	req := authenticatedBootstrapRequest(t, deps.bootstrapAuth, response)
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	guard(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("SetParameterValuesResponse bootstrap status = %d body=%q, want 204", recorder.Code, recorder.Body.String())
	}
	if cred.Status != credentials.StatusPending {
		t.Fatalf("bootstrap RPC acknowledgement changed credential to %q, want PENDING until unique bound reconnect", cred.Status)
	}
}

func TestInferBootstrapDataModelRoot(t *testing.T) {
	cases := []struct {
		name   string
		params []cwmp.ParameterValueStruct
		want   string
	}{
		{name: "device2", params: []cwmp.ParameterValueStruct{{Name: "Device.DeviceInfo.SoftwareVersion"}}, want: devices.DataModelRootDevice2},
		{name: "igd1", params: []cwmp.ParameterValueStruct{{Name: "InternetGatewayDevice.DeviceInfo.SoftwareVersion"}}, want: devices.DataModelRootIGD1},
		{name: "unknown", params: nil, want: devices.DataModelRootUnknown},
		{name: "conflicting", params: []cwmp.ParameterValueStruct{{Name: "Device.DeviceInfo.SoftwareVersion"}, {Name: "InternetGatewayDevice.DeviceInfo.SoftwareVersion"}}, want: devices.DataModelRootUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferBootstrapDataModelRoot(tc.params); got != tc.want {
				t.Fatalf("inferBootstrapDataModelRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}
