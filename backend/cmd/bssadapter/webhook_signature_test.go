package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
