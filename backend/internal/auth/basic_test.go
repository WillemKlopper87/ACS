package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func basicRequest(t *testing.T, user, pass string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	return r
}

func TestVerify_BasicAcceptedWhenEnabled(t *testing.T) {
	d := DigestAuthenticator{Username: "cpe", Password: "secret", AllowBasic: true}
	if ok, _, _ := d.Verify(basicRequest(t, "cpe", "secret")); !ok {
		t.Fatal("valid Basic credentials rejected with AllowBasic=true")
	}
	if ok, _, _ := d.Verify(basicRequest(t, "cpe", "wrong")); ok {
		t.Fatal("invalid Basic password accepted")
	}
	if ok, _, _ := d.Verify(basicRequest(t, "other", "secret")); ok {
		t.Fatal("invalid Basic username accepted")
	}
}

func TestVerify_BasicRejectedWhenDisabled(t *testing.T) {
	d := DigestAuthenticator{Username: "cpe", Password: "secret"}
	if ok, _, _ := d.Verify(basicRequest(t, "cpe", "secret")); ok {
		t.Fatal("Basic credentials accepted without AllowBasic")
	}
}

func TestChallenge_OffersBasicWhenEnabled(t *testing.T) {
	d := DigestAuthenticator{Username: "cpe", Password: "secret", AllowBasic: true}
	rec := httptest.NewRecorder()
	d.Challenge(rec)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Challenge status = %d, want 401", rec.Code)
	}
	values := rec.Header().Values("WWW-Authenticate")
	var sawDigest, sawBasic bool
	for _, v := range values {
		if len(v) >= 6 && v[:6] == "Digest" {
			sawDigest = true
		}
		if len(v) >= 5 && v[:5] == "Basic" {
			sawBasic = true
		}
	}
	if !sawDigest || !sawBasic {
		t.Fatalf("Challenge headers = %v, want both Digest and Basic", values)
	}
}
