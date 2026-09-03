package connreq

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAttemptPlain200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second, nil)
	if got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttemptAccepts204AndOther2xx(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusAccepted} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			if got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second, nil); got != OutcomeHTTP200 {
				t.Fatalf("Attempt() = %q, want normalized success %q", got, OutcomeHTTP200)
			}
		})
	}
}

func TestAttempt404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second, nil)
	if got != OutcomeHTTP404 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP404)
	}
}

func TestAttemptUnavailableWithoutURL(t *testing.T) {
	got := Attempt(t.Context(), "", "user", "pass", time.Second, nil)
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

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", time.Second, nil)
	if got != OutcomeHTTP401 {
		t.Errorf("Attempt() = %q, want %q (no retry without credentials)", got, OutcomeHTTP401)
	}
}

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
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil)
	if got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
	if requestCount != 2 {
		t.Errorf("requestCount = %d, want 2 (initial + digest retry)", requestCount)
	}
}

func TestAttemptDigestQOPListCaseInsensitiveAndOpaque(t *testing.T) {
	const username, password = "user", "pass"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `dIgEsT realm="cpe", qop="auth,auth-int", nonce="n1", opaque="keep-me", algorithm=md5`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.Contains(auth, "qop=auth") || !strings.Contains(auth, `opaque="keep-me"`) {
			t.Fatalf("Authorization missing compatible qop/opaque fields: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Fatalf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttemptBasicFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="legacy-cpe"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if user != "user" || pass != "pass" {
			t.Fatalf("unexpected Basic credentials %q/%q", user, pass)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if got := Attempt(t.Context(), server.URL+"/cwmp", "user", "pass", time.Second, nil); got != OutcomeHTTP200 {
		t.Fatalf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttemptDigestWrongPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="cpe", qop="auth", nonce="n1"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	got := Attempt(t.Context(), server.URL+"/cwmp", "user", "wrong", time.Second, nil)
	if got != OutcomeHTTP401 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP401)
	}
}

func TestAttemptConnectionRefused(t *testing.T) {
	got := Attempt(t.Context(), "http://127.0.0.1:1/cwmp", "", "", 500*time.Millisecond, nil)
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

	got := Attempt(t.Context(), server.URL+"/cwmp", "", "", 20*time.Millisecond, nil)
	if got != OutcomeTimeout {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeTimeout)
	}
}
