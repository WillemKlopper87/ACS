package main

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/devices"
)

const bootstrapInformXML = `<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-2">
  <soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">bootstrap-1</cwmp:ID></soap-env:Header>
  <soap-env:Body>
    <cwmp:Inform>
      <DeviceId>
        <Manufacturer>Huawei</Manufacturer>
        <OUI>A1B2C3</OUI>
        <ProductClass>N5368X</ProductClass>
        <SerialNumber>BOOTSTRAP-SERIAL-1</SerialNumber>
      </DeviceId>
      <Event></Event>
      <MaxEnvelopes>1</MaxEnvelopes>
      <CurrentTime>2026-09-15T20:00:00Z</CurrentTime>
      <RetryCount>0</RetryCount>
      <ParameterList></ParameterList>
    </cwmp:Inform>
  </soap-env:Body>
</soap-env:Envelope>`

func testBootstrapAuthenticator() auth.DigestAuthenticator {
	return auth.DigestAuthenticator{
		Username:    "cwmp-bootstrap",
		Password:    "bootstrap-password-material-32b",
		NonceSecret: []byte("shared-nonce-key-material-32bytes"),
		Algorithms:  []string{"MD5"},
	}
}

func bootstrapMD5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func digestChallengeDirective(header, name string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	for _, directive := range strings.Split(parts[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(directive), "=", 2)
		if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), name) {
			return strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
	}
	return ""
}

func bootstrapAuthorization(t *testing.T, authenticator auth.DigestAuthenticator, method, uri string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	authenticator.Challenge(recorder)
	challenges := recorder.Header().Values("WWW-Authenticate")
	if len(challenges) != 1 {
		t.Fatalf("bootstrap challenge count = %d, want 1", len(challenges))
	}
	nonce := digestChallengeDirective(challenges[0], "nonce")
	if nonce == "" {
		t.Fatalf("bootstrap challenge missing nonce: %q", challenges[0])
	}

	nc := "00000001"
	cnonce := "bootstrap-test-cnonce"
	ha1 := bootstrapMD5Hex(authenticator.Username + ":acs:" + authenticator.Password)
	ha2 := bootstrapMD5Hex(method + ":" + uri)
	response := bootstrapMD5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	return `Digest username="` + authenticator.Username + `", realm="acs", nonce="` + nonce +
		`", uri="` + uri + `", qop=auth, nc=` + nc + `, cnonce="` + cnonce +
		`", response="` + response + `", algorithm=MD5`
}

func authenticatedBootstrapRequest(t *testing.T, authenticator auth.DigestAuthenticator, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://acs.example/cwmp", strings.NewReader(body))
	req.Header.Set("Authorization", bootstrapAuthorization(t, authenticator, http.MethodPost, "/cwmp"))
	return req
}

func TestBootstrapGuardAcceptsUnregisteredInformWithoutEnteringNormalHandler(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	nextCalls := 0
	lookupCalls := 0
	guard := bootstrapCWMPGuardWithDeps(
		func(w http.ResponseWriter, _ *http.Request) {
			nextCalls++
			http.Error(w, "normal handler must not run", http.StatusTeapot)
		},
		authenticator,
		nil,
		nil,
		nil,
		func(_ context.Context, naturalKey string) (*devices.Device, error) {
			lookupCalls++
			if naturalKey != "A1B2C3_N5368X_BOOTSTRAP-SERIAL-1" {
				t.Fatalf("natural key = %q, want normalized Inform identity", naturalKey)
			}
			return nil, sql.ErrNoRows
		},
	)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, authenticator, bootstrapInformXML))

	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap Inform status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "InformResponse") {
		t.Fatalf("bootstrap response = %q, want InformResponse", recorder.Body.String())
	}
	if nextCalls != 0 {
		t.Fatalf("normal handler called %d times; bootstrap must never enter session/job dispatch", nextCalls)
	}
	if lookupCalls != 1 {
		t.Fatalf("device identity lookup calls = %d, want exactly 1 before response", lookupCalls)
	}
	if cookie := recorder.Header().Get("Set-Cookie"); cookie != "" {
		t.Fatalf("bootstrap unexpectedly created a session cookie: %q", cookie)
	}
}

func TestBootstrapGuardRejectsEstablishedDeviceAcrossTenantBoundary(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	nextCalls := 0
	now := time.Now()
	customerID := "other-tenant"
	guard := bootstrapCWMPGuardWithDeps(
		func(http.ResponseWriter, *http.Request) { nextCalls++ },
		authenticator,
		nil,
		nil,
		nil,
		func(context.Context, string) (*devices.Device, error) {
			return &devices.Device{
				ID:           "established-device",
				CustomerID:   &customerID,
				LastInformAt: &now,
				CWMPAuthMode: devices.AuthModeDigest,
			}, nil
		},
	)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, authenticator, bootstrapInformXML))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("established-device bootstrap status = %d body=%q, want 403", recorder.Code, recorder.Body.String())
	}
	if nextCalls != 0 {
		t.Fatalf("normal handler called %d times for established-device bootstrap", nextCalls)
	}
}

func TestBootstrapGuardAllowsPreRegisteredButUnmanagedIdentity(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	customerID := "preassigned-tenant"
	guard := bootstrapCWMPGuardWithDeps(
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unexpected normal handler", http.StatusTeapot)
		},
		authenticator,
		nil,
		nil,
		nil,
		func(context.Context, string) (*devices.Device, error) {
			return &devices.Device{ID: "pre-registered", CustomerID: &customerID, CWMPAuthMode: devices.AuthModeNone}, nil
		},
	)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, authenticator, bootstrapInformXML))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pre-registered bootstrap status = %d body=%q, want 200", recorder.Code, recorder.Body.String())
	}
}

func TestBootstrapGuardRejectsNonInformAndNeverConsumesNormalWork(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	nextCalls := 0
	guard := bootstrapCWMPGuardWithDeps(
		func(http.ResponseWriter, *http.Request) { nextCalls++ },
		authenticator,
		nil,
		nil,
		nil,
		func(context.Context, string) (*devices.Device, error) {
			t.Fatal("device lookup must not run for a non-Inform bootstrap request")
			return nil, nil
		},
	)

	nonInform := `<Envelope><Header><ID>bootstrap-2</ID></Header><Body><RebootResponse/></Body></Envelope>`
	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, authenticator, nonInform))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("bootstrap non-Inform status = %d body=%q, want 403", recorder.Code, recorder.Body.String())
	}
	if nextCalls != 0 {
		t.Fatalf("normal handler called %d times; bootstrap must not reach queued work", nextCalls)
	}
}

func TestBootstrapGuardClosesEmptyFollowUpWithoutSessionDispatch(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	nextCalls := 0
	guard := bootstrapCWMPGuardWithDeps(
		func(http.ResponseWriter, *http.Request) { nextCalls++ },
		authenticator,
		nil,
		nil,
		nil,
		func(context.Context, string) (*devices.Device, error) {
			t.Fatal("device lookup must not run for empty bootstrap follow-up")
			return nil, nil
		},
	)

	recorder := httptest.NewRecorder()
	guard(recorder, authenticatedBootstrapRequest(t, authenticator, " \r\n "))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("empty bootstrap follow-up status = %d, want 204", recorder.Code)
	}
	if nextCalls != 0 {
		t.Fatalf("normal handler called %d times for empty bootstrap follow-up", nextCalls)
	}
}

func TestBootstrapGuardDoesNotCaptureNormalDeviceCredential(t *testing.T) {
	authenticator := testBootstrapAuthenticator()
	nextCalls := 0
	lookupCalls := 0
	guard := bootstrapCWMPGuardWithDeps(
		func(w http.ResponseWriter, _ *http.Request) {
			nextCalls++
			w.WriteHeader(http.StatusAccepted)
		},
		authenticator,
		nil,
		nil,
		nil,
		func(context.Context, string) (*devices.Device, error) {
			lookupCalls++
			return nil, nil
		},
	)

	req := httptest.NewRequest(http.MethodPost, "http://acs.example/cwmp", strings.NewReader(bootstrapInformXML))
	req.Header.Set("Authorization", `Digest username="unique-device-credential"`)
	recorder := httptest.NewRecorder()
	guard(recorder, req)
	if recorder.Code != http.StatusAccepted || nextCalls != 1 {
		t.Fatalf("normal credential was intercepted: status=%d nextCalls=%d", recorder.Code, nextCalls)
	}
	if lookupCalls != 0 {
		t.Fatalf("bootstrap lookup called %d times for normal credential", lookupCalls)
	}
}

func TestBootstrapDeviceEstablishedClassification(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		dev  *devices.Device
		want bool
	}{
		{name: "missing", dev: nil, want: false},
		{name: "pre-registered", dev: &devices.Device{CWMPAuthMode: devices.AuthModeNone}, want: false},
		{name: "prior Inform", dev: &devices.Device{LastInformAt: &now, CWMPAuthMode: devices.AuthModeNone}, want: true},
		{name: "recorded Digest auth", dev: &devices.Device{CWMPAuthMode: devices.AuthModeDigest}, want: true},
		{name: "recorded mTLS auth", dev: &devices.Device{CWMPAuthMode: devices.AuthModeMTLS}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bootstrapDeviceIsEstablished(tc.dev); got != tc.want {
				t.Fatalf("bootstrapDeviceIsEstablished() = %v, want %v", got, tc.want)
			}
		})
	}
}
