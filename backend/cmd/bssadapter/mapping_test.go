package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"acs/internal/bss"
	"acs/internal/observability"
	"acs/internal/store"
)

// newMappingTestHandler mirrors internal/bss's newMappingTestRepo: a
// clean, fully migrated schema per test, wired into just enough of
// bssadapter's handler to exercise createMapping. It also hands back the
// raw *sql.DB so the test can seed a devices row directly, the same way
// internal/bss/mapping_test.go's seedDevice does.
func newMappingTestHandler(t *testing.T) (context.Context, *handler, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, &handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		mappings: bss.NewRepository(db),
		auditor:  observability.NewAuditor(db),
	}, db
}

// TestCreateMapping_DeviceUUIDMismatchLeavesNoAssignment is item 1's
// regression test: a createMapping call whose device_uuid does not match
// what oui_serial actually resolves to must be rejected with 400 *before*
// any row is written. Under the old order (AssignDevice first, validate
// after) the row was already committed by the time the 400 was returned,
// wedging the role slot — the caller's corrected retry got 409
// ErrRoleAlreadyAssigned for a mapping the rejected request itself made.
func TestCreateMapping_DeviceUUIDMismatchLeavesNoAssignment(t *testing.T) {
	ctx, h, db := newMappingTestHandler(t)

	const (
		accountID = "acct-1"
		ouiSerial = "AABBCC-SERIAL-1"
		deviceID  = "11111111-1111-1111-1111-111111111111"
		wrongUUID = "99999999-9999-9999-9999-999999999999"
	)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, deviceID, ouiSerial); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	body, _ := json.Marshal(createMappingRequest{
		AccountID:  accountID,
		OUISerial:  ouiSerial,
		DeviceUUID: wrongUUID,
	})
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/mappings", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.createMapping(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}

	// The role slot (gateway, the default) must still be empty: no row was
	// committed before the mismatch was caught.
	_, err := h.mappings.ActiveDeviceForAccount(ctx, accountID, bss.RoleGateway)
	if !errors.Is(err, bss.ErrNoDeviceForRole) {
		t.Errorf("ActiveDeviceForAccount after rejected mismatch = %v, want ErrNoDeviceForRole (mapping must not have been created)", err)
	}
}
