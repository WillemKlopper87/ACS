package connreq

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttemptPlain200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second)
	if got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttempt404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second)
	if got != OutcomeHTTP404 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP404)
	}
}

func TestAttemptUnavailableWithoutURL(t *testing.T) {
	got := Attempt(t.Context(), "", "user", "pass", time.Second)
	if got != OutcomeUnavailable {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeUnavailable)
	}
}

func TestAttempt401WithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="cpe", qop="auth", nonce="abc123"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second)
	if got != OutcomeHTTP401 {
		t.Errorf("Attempt() = %q, want %q (no retry without credentials)", got, OutcomeHTTP401)
	}
}

// TestAttemptDigestChallengeSuccess acts as a minimal mock CPE Connection
// Request endpoint: challenges the first request with Digest, then
// verifies the retried request's Authorization header is a correctly
// computed Digest response before accepting it.
func TestAttemptDigestChallengeSuccess(t *testing.T) {
	const username, password, realm, nonce = "cpe-acs", "s3cret", "cpe", "fixed-test-nonce"
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, realm, nonce))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		params := parseChallengeParams(auth[len("Digest "):])
		wantHA1 := md5Hex(username + ":" + realm + ":" + password)
		wantHA2 := md5Hex(http.MethodGet + ":" + r.URL.RequestURI())
		wantResponse := md5Hex(wantHA1 + ":" + nonce + ":" + params["nc"] + ":" + params["cnonce"] + ":auth:" + wantHA2)

		if params["username"] != username || params["response"] != wantResponse {
			t.Errorf("unexpected digest response: params=%+v want response=%q", params, wantResponse)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second)
	if got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
	if requestCount != 2 {
		t.Errorf("requestCount = %d, want 2 (initial + digest retry)", requestCount)
	}
}

func TestAttemptDigestWrongPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="cpe", qop="auth", nonce="n1"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Any retried request is still treated as wrong credentials for this test.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "user", "wrong", time.Second)
	if got != OutcomeHTTP401 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP401)
	}
}

func TestAttemptConnectionRefused(t *testing.T) {
	got := Attempt(t.Context(), "http://127.0.0.1:1/cwmp", "", "", 500*time.Millisecond)
	if got != OutcomeTCPFailure {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeTCPFailure)
	}
}

func TestAttemptTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", 20*time.Millisecond)
	if got != OutcomeTimeout {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeTimeout)
	}
}
