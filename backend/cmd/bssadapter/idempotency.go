package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"acs/internal/bss"
)

const maxIdempotencyKeyLen = 255

// idempotent runs a write once per Idempotency-Key (optional header) for
// an account the caller is already authorized for. A retry with the same
// key and request gets the stored status, body and Location back with
// Idempotent-Replayed: true; the same key with another request is 409
// ErrIdempotencyKeyReused; a retry while the first is still running is 409
// ErrIdempotencyKeyInFlight with Retry-After. A 5xx is not stored, so the
// retry runs again. Without the header the write just runs.
func (h *handler) idempotent(w http.ResponseWriter, r *http.Request, accountID, operation string, req any, run func(http.ResponseWriter)) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		run(w)
		return
	}
	if len(key) > maxIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "Idempotency-Key is longer than 255 characters")
		return
	}
	canonical, _ := json.Marshal(req)
	sum := sha256.Sum256(canonical)
	stored, err := h.mappings.BeginIdempotent(r.Context(), accountID, key, operation, hex.EncodeToString(sum[:]))
	switch {
	case errors.Is(err, bss.ErrIdempotencyMismatch):
		writeError(w, http.StatusConflict, "ErrIdempotencyKeyReused", "this Idempotency-Key was already used for a different request")
		return
	case errors.Is(err, bss.ErrIdempotencyInFlight):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusConflict, "ErrIdempotencyKeyInFlight", "a request with this Idempotency-Key is still being processed")
		return
	case err != nil:
		h.logger.Error("failed to check idempotency key", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	case stored != nil:
		if stored.Location != "" {
			w.Header().Set("Location", stored.Location)
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stored.Status)
		_, _ = w.Write(stored.Body)
		return
	}

	rec := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
	run(rec)
	// Store with a fresh context: the client hanging up must not leave the
	// key in flight forever.
	ctx := context.WithoutCancel(r.Context())
	if rec.status >= 500 {
		if err := h.mappings.ReleaseIdempotent(ctx, accountID, key); err != nil {
			h.logger.Error("failed to release idempotency key", "err", err, "account_id", accountID)
		}
		return
	}
	if err := h.mappings.CompleteIdempotent(ctx, accountID, key, bss.IdempotentResponse{
		Status: rec.status, Body: bytes.TrimSpace(rec.body.Bytes()), Location: rec.Header().Get("Location"),
	}); err != nil {
		h.logger.Error("failed to store idempotent response", "err", err, "account_id", accountID)
	}
}

type capturingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capturingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

func (c *capturingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
