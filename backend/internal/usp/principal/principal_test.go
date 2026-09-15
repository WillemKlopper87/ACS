package principal

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"testing"

	"acs/internal/devices"
	"acs/internal/store"
	"acs/internal/usp"
)

func TestCertificateSHA256StableAndDistinct(t *testing.T) {
	a := &x509.Certificate{Raw: []byte("certificate-a-der")}
	b := &x509.Certificate{Raw: []byte("certificate-b-der")}

	gotA1 := CertificateSHA256(a)
	gotA2 := CertificateSHA256(a)
	gotB := CertificateSHA256(b)
	if gotA1 == "" || len(gotA1) != 64 {
		t.Fatalf("fingerprint = %q, want 64-char sha256 hex", gotA1)
	}
	if gotA1 != gotA2 {
		t.Fatalf("same certificate fingerprint changed: %q != %q", gotA1, gotA2)
	}
	if gotA1 == gotB {
		t.Fatal("different certificate DER produced the same fingerprint")
	}
	if CertificateSHA256(nil) != "" {
		t.Fatal("nil certificate fingerprint is non-empty")
	}
}

func TestBindCertificateRejectsUnsafeBinding(t *testing.T) {
	r := &Repository{}
	cert := &x509.Certificate{Raw: []byte("cert")}
	cases := []struct {
		name     string
		deviceID string
		endpoint usp.EndpointID
		topic    string
		cert     *x509.Certificate
	}{
		{name: "blank device", endpoint: "os::agent", topic: "/usp/agent", cert: cert},
		{name: "blank endpoint", deviceID: "device", topic: "/usp/agent", cert: cert},
		{name: "nil cert", deviceID: "device", endpoint: "os::agent", topic: "/usp/agent"},
		{name: "blank topic", deviceID: "device", endpoint: "os::agent", cert: cert},
		{name: "hash wildcard", deviceID: "device", endpoint: "os::agent", topic: "/usp/#", cert: cert},
		{name: "plus wildcard", deviceID: "device", endpoint: "os::agent", topic: "/usp/+/reply", cert: cert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.BindCertificate(context.Background(), tc.deviceID, tc.endpoint, tc.cert, tc.topic); !errors.Is(err, ErrInvalidBinding) {
				t.Fatalf("BindCertificate error = %v, want ErrInvalidBinding", err)
			}
		})
	}
}

func newPrincipalTestRepo(t *testing.T) (context.Context, *Repository, *devices.Repository) {
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
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, NewRepository(db), devices.NewRepository(db)
}

func seedPrincipalDevice(t *testing.T, ctx context.Context, r *devices.Repository, serial string) string {
	t.Helper()
	oui := "001122"
	productClass := "Router"
	naturalKey := oui + "-" + productClass + "-" + serial
	d, err := r.PreRegister(ctx, naturalKey, "", oui, productClass, serial, nil, nil)
	if err != nil {
		t.Fatalf("PreRegister: %v", err)
	}
	return d.ID
}

func TestCertificatePrincipalAuthenticateRotateAndDisable(t *testing.T) {
	ctx, principals, devicesRepo := newPrincipalTestRepo(t)
	deviceID := seedPrincipalDevice(t, ctx, devicesRepo, "PRINCIPAL-01")
	certA := &x509.Certificate{Raw: []byte("principal-cert-a")}
	certB := &x509.Certificate{Raw: []byte("principal-cert-b")}

	bound, err := principals.BindCertificate(ctx, deviceID, "os::001122-PRINCIPAL-01", certA, "/usp/agent/principal-01")
	if err != nil {
		t.Fatalf("BindCertificate A: %v", err)
	}
	if bound.DeviceID != deviceID || bound.EndpointID != "os::001122-PRINCIPAL-01" || bound.MQTTTopic != "/usp/agent/principal-01" || !bound.Enabled {
		t.Fatalf("bound principal = %+v", bound)
	}

	authenticated, err := principals.AuthenticateCertificate(ctx, certA)
	if err != nil {
		t.Fatalf("AuthenticateCertificate A: %v", err)
	}
	if authenticated.DeviceID != deviceID || authenticated.EndpointID != bound.EndpointID {
		t.Fatalf("authenticated principal = %+v, want device %s endpoint %s", authenticated, deviceID, bound.EndpointID)
	}

	// Rotation is atomic at the device row: the new certificate replaces
	// the old trust anchor rather than adding a second valid principal.
	if _, err := principals.BindCertificate(ctx, deviceID, bound.EndpointID, certB, bound.MQTTTopic); err != nil {
		t.Fatalf("BindCertificate B: %v", err)
	}
	if _, err := principals.AuthenticateCertificate(ctx, certA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old certificate after rotation = %v, want ErrNotFound", err)
	}
	if _, err := principals.AuthenticateCertificate(ctx, certB); err != nil {
		t.Fatalf("new certificate after rotation rejected: %v", err)
	}

	if err := principals.Disable(ctx, deviceID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := principals.AuthenticateCertificate(ctx, certB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled certificate = %v, want ErrNotFound", err)
	}
	stored, err := principals.ByDeviceID(ctx, deviceID)
	if err != nil {
		t.Fatalf("ByDeviceID after disable: %v", err)
	}
	if stored.Enabled {
		t.Fatal("stored principal still enabled after Disable")
	}
}
