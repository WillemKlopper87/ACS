package main

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/store"
	"acs/internal/usp/principal"
	"acs/internal/uspprincipal"
)

// TestUSPPrincipalRepositoryProductionPath is intentionally in cmd/uspc rather
// than only internal/uspprincipal: the migration CI job explicitly runs this
// package with ACS_TEST_POSTGRES_DSN, while the broad unit/race job has no test
// database and therefore skips DB-backed repository tests. This makes the
// production certificate->device principal persistence a real CI gate.
func TestUSPPrincipalRepositoryProductionPath(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	identity := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "USPC-PRINCIPAL-CI"}
	device, err := devices.NewRepository(db).PreRegister(ctx, identity.NaturalKey(), "", identity.OUI, identity.ProductClass, identity.SerialNumber, nil, nil)
	if err != nil {
		t.Fatalf("pre-register device: %v", err)
	}

	repo := uspprincipal.NewRepository(db)
	certA := &x509.Certificate{Raw: []byte("uspc-production-principal-cert-a")}
	certB := &x509.Certificate{Raw: []byte("uspc-production-principal-cert-b")}
	endpoint := principal.Principal{EndpointID: "os::001122-USPC-PRINCIPAL-CI"}.EndpointID
	topic := "/usp/agent/uspc-principal-ci"

	bound, err := repo.BindCertificate(ctx, device.ID, endpoint, certA, topic)
	if err != nil {
		t.Fatalf("BindCertificate A: %v", err)
	}
	if bound.DeviceID != device.ID || bound.EndpointID != endpoint || bound.MQTTTopic != topic || !bound.Enabled {
		t.Fatalf("bound principal = %+v", bound)
	}
	if got, err := repo.AuthenticateCertificate(ctx, certA); err != nil || got.DeviceID != device.ID {
		t.Fatalf("AuthenticateCertificate A = %+v, %v; want device %s", got, err, device.ID)
	}

	if _, err := repo.BindCertificate(ctx, device.ID, endpoint, certB, topic); err != nil {
		t.Fatalf("BindCertificate B: %v", err)
	}
	if _, err := repo.AuthenticateCertificate(ctx, certA); !errors.Is(err, principal.ErrNotFound) {
		t.Fatalf("old certificate after rotation = %v, want principal.ErrNotFound", err)
	}
	if _, err := repo.AuthenticateCertificate(ctx, certB); err != nil {
		t.Fatalf("new certificate after rotation rejected: %v", err)
	}

	if err := repo.Disable(ctx, device.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := repo.AuthenticateCertificate(ctx, certB); !errors.Is(err, principal.ErrNotFound) {
		t.Fatalf("disabled principal authenticated: %v", err)
	}
}
