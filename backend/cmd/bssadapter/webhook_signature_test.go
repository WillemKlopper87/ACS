package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// TestWebhookSignatureCoversIDAndTimestamp is the whole point of the
// change: a body-only HMAC is replayable forever, because the consumer has
// nothing to age-check or dedupe against. The signed string must bind the
// delivery id and the send time to the payload.
func TestWebhookSignatureCoversIDAndTimestamp(t *testing.T) {
	const (
		secret  = "whsec_test_secret_value_at_least_24b"
		msgID   = "3f7c1b9e-0000-4000-8000-000000000001"
		tsOne   = "1788950000"
		tsTwo   = "1788950001"
		payload = `{"event_type":"order.completed"}`
	)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID + "." + tsOne + "." + payload))
	want := hex.EncodeToString(mac.Sum(nil))

	got := webhookSignature(secret, msgID, tsOne, []byte(payload))
	if got != want {
		t.Errorf("webhookSignature = %q, want %q", got, want)
	}

	// A body-only HMAC must NOT match — that is the bug being fixed.
	bodyOnly := hmac.New(sha256.New, []byte(secret))
	bodyOnly.Write([]byte(payload))
	if got == hex.EncodeToString(bodyOnly.Sum(nil)) {
		t.Error("signature equals a body-only HMAC; id and timestamp are not bound in")
	}

	// Changing only the timestamp must change the signature, or replay
	// protection is decorative.
	if webhookSignature(secret, msgID, tsTwo, []byte(payload)) == got {
		t.Error("signature is unchanged when the timestamp changes")
	}

	// Changing only the delivery id must change the signature.
	if webhookSignature(secret, "3f7c1b9e-0000-4000-8000-000000000002", tsOne, []byte(payload)) == got {
		t.Error("signature is unchanged when the delivery id changes")
	}
}

// TestWebhookSignatureIsWireCompatibleWithStandardWebhooks proves a real
// whsec_<base64> secret (the shape svix/standardwebhooks actually
// generates, unlike this file's other fixture which merely starts with
// "whsec_" without valid base64 after it) gets the genuine Standard
// Webhooks wire format: the key is base64-decoded, not used as raw string
// bytes, and the MAC is base64-encoded, not hex.
func TestWebhookSignatureIsWireCompatibleWithStandardWebhooks(t *testing.T) {
	const (
		secret  = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
		msgID   = "msg_p5jXN8AQM9LWM0D4loKWxJek"
		ts      = "1788950000"
		payload = `{"event_type":"order.completed"}`
	)
	keyB64 := strings.TrimPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		t.Fatalf("test fixture secret is not valid base64: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msgID + "." + ts + "." + payload))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	got := webhookSignature(secret, msgID, ts, []byte(payload))
	if got != want {
		t.Errorf("webhookSignature = %q, want %q (base64, decoded-key HMAC)", got, want)
	}
	if _, err := hex.DecodeString(got); err == nil && len(got) == 64 {
		t.Error("signature looks hex-encoded; a real whsec_ secret must produce base64 output")
	}
}

// TestWebhookSecretKeyMaterialFallsBackOnBadBase64 covers the malformed
// case explicitly: a secret that merely starts with "whsec_" (like this
// file's first fixture) without valid base64 after it must not silently
// produce garbage key material — it falls back to the original raw-bytes,
// hex-output behavior instead.
func TestWebhookSecretKeyMaterialFallsBackOnBadBase64(t *testing.T) {
	key, base64Output := webhookSecretKeyMaterial("whsec_not_valid_base64!!!")
	if base64Output {
		t.Error("expected fallback to hex/raw mode for invalid base64")
	}
	if string(key) != "whsec_not_valid_base64!!!" {
		t.Errorf("fallback key = %q, want the original secret used verbatim", key)
	}
}

// TestGenerateWebhookSecretProducesValidStandardWebhooksFormat proves the
// generator's own output round-trips through the exact same
// webhookSecretKeyMaterial path a real integrator's secret would.
func TestGenerateWebhookSecretProducesValidStandardWebhooksFormat(t *testing.T) {
	secret, err := generateWebhookSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("generated secret = %q, want a whsec_ prefix", secret)
	}
	_, base64Output := webhookSecretKeyMaterial(secret)
	if !base64Output {
		t.Errorf("generated secret %q did not resolve to the base64-output Standard Webhooks path", secret)
	}
}
