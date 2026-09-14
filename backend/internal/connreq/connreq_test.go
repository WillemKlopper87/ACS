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

// --- SHA-256 Digest (RFC 7616) -----------------------------------------

// digestCPE is a fake CPE that challenges with one algorithm and verifies
// the response with the matching hash — the mirror of what a Huawei
// N5368X set to "Digest-SHA256" does to an incoming Connection Request.
func digestCPE(t *testing.T, challenge, username, password, realm, nonce string, h func(string) string, sess bool) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(auth, "Digest ") {
			t.Errorf("Authorization = %q, want a Digest response", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		params := parseChallengeParams(auth[len("Digest "):])
		ha1 := h(username + ":" + realm + ":" + password)
		if sess {
			ha1 = h(ha1 + ":" + nonce + ":" + params["cnonce"])
		}
		ha2 := h(http.MethodGet + ":" + r.URL.RequestURI())
		want := h(strings.Join([]string{ha1, nonce, params["nc"], params["cnonce"], "auth", ha2}, ":"))
		if params["response"] != want {
			t.Errorf("digest response = %q, want %q (params=%+v)", params["response"], want, params)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestAttemptDigestSHA256(t *testing.T) {
	const username, password, realm, nonce = "acs-connreq", "s3cret", "cpe", "n-sha256"
	server, requests := digestCPE(t,
		fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=SHA-256`, realm, nonce),
		username, password, realm, nonce, sha256Hex, false)

	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q — a SHA-256-only CPE could not be woken", got, OutcomeHTTP200)
	}
	if *requests != 2 {
		t.Errorf("requests = %d, want 2 (initial + digest retry)", *requests)
	}
}

func TestAttemptDigestSHA256Sess(t *testing.T) {
	const username, password, realm, nonce = "acs-connreq", "s3cret", "cpe", "n-sha256-sess"
	server, _ := digestCPE(t,
		fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=SHA-256-sess`, realm, nonce),
		username, password, realm, nonce, sha256Hex, true)

	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttemptDigestMD5SessStillWorks(t *testing.T) {
	const username, password, realm, nonce = "acs-connreq", "s3cret", "cpe", "n-md5-sess"
	server, _ := digestCPE(t,
		fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5-sess`, realm, nonce),
		username, password, realm, nonce, md5Hex, true)

	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

// A CPE offering both must get the stronger one: it has already said it
// can verify either, so there is nothing to lose by not sending MD5.
func TestAttemptPrefersSHA256OverMD5(t *testing.T) {
	const username, password, realm, nonce = "acs-connreq", "s3cret", "cpe", "n-both"
	var sawAlgorithm string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// MD5 listed first, as an RFC 2617-era CPE would.
			w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, realm, nonce))
			w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=SHA-256`, realm, nonce))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		params := parseChallengeParams(auth[len("Digest "):])
		sawAlgorithm = params["algorithm"]
		ha1 := sha256Hex(username + ":" + realm + ":" + password)
		ha2 := sha256Hex(http.MethodGet + ":" + r.URL.RequestURI())
		want := sha256Hex(strings.Join([]string{ha1, nonce, params["nc"], params["cnonce"], "auth", ha2}, ":"))
		if params["response"] != want {
			t.Errorf("response was not computed with SHA-256: %+v", params)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Fatalf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
	if sawAlgorithm != "SHA-256" {
		t.Errorf("answered algorithm=%q, want SHA-256 echoed back verbatim", sawAlgorithm)
	}
}

// An algorithm we don't implement must not be answered with a response
// computed under a different hash — the CPE would read that as a wrong
// password. Falling through to Basic, when the CPE offers it, is correct.
func TestAttemptUnsupportedAlgorithmFallsBackToBasic(t *testing.T) {
	const username, password = "acs-connreq", "s3cret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Add("WWW-Authenticate", `Digest realm="cpe", qop="auth", nonce="n1", algorithm=SHA-512-256`)
			w.Header().Add("WWW-Authenticate", `Basic realm="cpe"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			t.Errorf("expected the Basic fallback, got %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if got := Attempt(t.Context(), server.URL+"/cwmp", username, password, time.Second, nil); got != OutcomeHTTP200 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP200)
	}
}

func TestAttemptUnsupportedAlgorithmWithNoFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("retried with %q, want no retry at all", r.Header.Get("Authorization"))
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="cpe", qop="auth", nonce="n1", algorithm=SHA-512-256`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	if got := Attempt(t.Context(), server.URL+"/cwmp", "acs-connreq", "s3cret", time.Second, nil); got != OutcomeHTTP401 {
		t.Errorf("Attempt() = %q, want %q", got, OutcomeHTTP401)
	}
}

func TestAlgorithmFamilyAndHashSelection(t *testing.T) {
	for _, tc := range []struct {
		algorithm string
		family    string
		sess      bool
		supported bool
	}{
		{"", "md5", false, true},
		{"MD5", "md5", false, true},
		{"md5", "md5", false, true},
		{"MD5-sess", "md5", true, true},
		{"SHA-256", "sha-256", false, true},
		{"sha-256", "sha-256", false, true},
		{" SHA-256 ", "sha-256", false, true},
		{"SHA-256-sess", "sha-256", true, true},
		{"SHA-512-256", "sha-512-256", false, false},
		{"bogus", "bogus", false, false},
	} {
		if got := algorithmFamily(tc.algorithm); got != tc.family {
			t.Errorf("algorithmFamily(%q) = %q, want %q", tc.algorithm, got, tc.family)
		}
		fn, sess, ok := hashForAlgorithm(tc.algorithm)
		if ok != tc.supported {
			t.Errorf("hashForAlgorithm(%q) supported = %v, want %v", tc.algorithm, ok, tc.supported)
			continue
		}
		if !ok {
			continue
		}
		if sess != tc.sess {
			t.Errorf("hashForAlgorithm(%q) sess = %v, want %v", tc.algorithm, sess, tc.sess)
		}
		want := md5Hex("x")
		if tc.family == "sha-256" {
			want = sha256Hex("x")
		}
		if fn("x") != want {
			t.Errorf("hashForAlgorithm(%q) returned the wrong hash function", tc.algorithm)
		}
	}
}
