package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"acs/internal/auth"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/ratelimit"
	"acs/internal/sessions"
	"acs/internal/store"
	"acs/internal/templates"
)

const (
	cpeUser = "cpe-device"
	cpePass = "integration-digest-password-0123456789"
)

type mockCPE struct {
	t      *testing.T
	client *http.Client
	url    string
	cookie *http.Cookie
	nonce  string
	nc     int
}

func (c *mockCPE) post(body string) (int, string) {
	c.t.Helper()
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader(body))
		req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
		if c.cookie != nil {
			req.AddCookie(c.cookie)
		}
		if c.nonce != "" {
			c.nc++
			req.Header.Set("Authorization", c.digest(http.MethodPost, "/cwmp"))
		}
		res, err := c.client.Do(req)
		if err != nil {
			c.t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		for _, ck := range res.Cookies() {
			if ck.Name == "acs_session" {
				c.cookie = ck
			}
		}
		if res.StatusCode == http.StatusUnauthorized {
			hdr := res.Header.Get("WWW-Authenticate")
			m := regexp.MustCompile(`nonce="([^"]+)"`).FindStringSubmatch(hdr)
			if m == nil {
				c.t.Fatalf("401 without a Digest nonce: %q", hdr)
			}
			c.nonce, c.nc = m[1], 0
			continue
		}
		return res.StatusCode, string(b)
	}
	c.t.Fatal("still unauthorized after answering the challenge")
	return 0, ""
}

func (c *mockCPE) digest(method, uri string) string {
	h := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	nc := fmt.Sprintf("%08x", c.nc)
	cnonce := "0a1b2c3d"
	ha1 := h(cpeUser + ":acs:" + cpePass)
	ha2 := h(method + ":" + uri)
	resp := h(strings.Join([]string{ha1, c.nonce, nc, cnonce, "auth", ha2}, ":"))
	return fmt.Sprintf(`Digest username="%s", realm="acs", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s", algorithm=MD5`,
		cpeUser, c.nonce, uri, nc, cnonce, resp)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../test/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func periodicInform(t *testing.T) string {
	return strings.Replace(fixture(t, "inform_bootstrap.xml"), "0 BOOTSTRAP", "2 PERIODIC", 1)
}

func TestIntegration_CPESession(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	metrics := observability.NewMetrics("acs-test")
	h := &handler{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		auth:          auth.DigestAuthenticator{Username: cpeUser, Password: cpePass},
		devices:       devices.NewRepository(db),
		sessions:      sessions.NewRepository(db),
		jobs:          jobs.NewRepository(db),
		params:        parameters.NewRepository(db),
		auditor:       observability.NewAuditor(db),
		metrics:       metrics,
		policies:      policy.NewRepository(db),
		templates:     templates.NewRepository(db),
		ipLimiter:     ratelimit.New(1000, 1000, time.Minute),
		deviceLimiter: ratelimit.New(1000, 1000, time.Minute),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleCWMP)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}

	code, body := cpe.post(periodicInform(t))
	if code != 200 || !strings.Contains(body, "InformResponse") {
		t.Fatalf("Inform → %d %s", code, body)
	}
	if cpe.cookie == nil {
		t.Fatal("no acs_session cookie after Inform")
	}
	if cpe.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("acs_session cookie SameSite = %v, want %v", cpe.cookie.SameSite, http.SameSiteLaxMode)
	}
	if cpe.cookie.HttpOnly != true {
		t.Fatal("acs_session cookie should be HttpOnly")
	}
	if !strings.Contains(body, "<cwmp:ID") || !strings.Contains(body, "cpe-0001") {
		t.Errorf("InformResponse must echo the request cwmp:ID: %s", body)
	}
	dev, err := h.devices.List(ctx, devices.ListParams{})
	if err != nil || dev.Total != 1 {
		t.Fatalf("device not registered from Inform: %v total=%d", err, dev.Total)
	}
	device := dev.Items[0]
	if device.Manufacturer != "Zyxel" || device.CWMPAuthMode != devices.AuthModeDigest {
		t.Errorf("device = %+v, want Zyxel via Digest", device)
	}
	cached, _ := h.params.Get(ctx, device.ID)
	if v, ok := cached["Device.DeviceInfo.SoftwareVersion"]; !ok || v.Value != "2.3.1" {
		t.Errorf("Inform parameters not cached: %+v", cached)
	}
	code, body = cpe.post("")
	if code != 200 && code != 204 {
		t.Fatalf("empty POST with no work → %d %s", code, body)
	}
	if open, _, _ := h.sessions.IsOpen(ctx, cpe.cookie.Value); open {
		t.Error("session should be closed after an empty POST with no work")
	}

	job, err := h.jobs.Create(ctx, device.ID, jobs.TypeSetParameter,
		jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "300", Type: "xsd:unsignedInt"}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	cpe.cookie = nil
	if code, _ = cpe.post(periodicInform(t)); code != 200 {
		t.Fatalf("second Inform → %d", code)
	}
	code, body = cpe.post("")
	if code != 200 || !strings.Contains(body, "SetParameterValues") || !strings.Contains(body, "PeriodicInformInterval") {
		t.Fatalf("expected SetParameterValues RPC after empty POST, got %d %s", code, body)
	}
	if !strings.Contains(body, job.CommandKey) {
		t.Errorf("RPC must carry the job's CommandKey %s: %s", job.CommandKey, body)
	}
	leased, _ := h.jobs.ByID(ctx, job.ID)
	if leased.Status != jobs.StatusRPCSent {
		t.Errorf("job status after dispatch = %s, want RPC_SENT", leased.Status)
	}
	code, _ = cpe.post(fixture(t, "set_parameter_values_response.xml"))
	if code != 200 && code != 204 {
		t.Fatalf("SPV response → %d", code)
	}
	done, _ := h.jobs.ByID(ctx, job.ID)
	if done.Status != jobs.StatusSuccess {
		t.Errorf("job status after SetParameterValuesResponse = %s, want SUCCESS (fault=%v)", done.Status, done.FaultString)
	}

	bad, _ := h.jobs.Create(ctx, device.ID, jobs.TypeSetParameter,
		jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.Nope", Value: "1", Type: "xsd:string"}}}, "test")
	cpe.cookie = nil
	cpe.post(periodicInform(t))
	if code, body = cpe.post(""); !strings.Contains(body, "SetParameterValues") {
		t.Fatalf("expected RPC dispatch, got %d %s", code, body)
	}
	cpe.post(fixture(t, "fault.xml"))
	failed, _ := h.jobs.ByID(ctx, bad.ID)
	if failed.Status != jobs.StatusFailed || failed.FaultCode == nil || *failed.FaultCode != "9005" {
		t.Errorf("job after CWMP fault = %+v, want FAILED with fault 9005", failed)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/cwmp", bytes.NewReader([]byte(periodicInform(t))))
	req.Header.Set("Authorization", cpe.digest(http.MethodPost, "/cwmp"))
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("replayed Digest header → %d, want 401", res.StatusCode)
	}
}

func TestRepository_RefreshLivenessTransitions(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed liveness regression test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	repo := devices.NewRepository(db)
	if _, err := repo.PreRegister(ctx, "LIVENESS-01", "TestVendor", "001349", "NR7101", "SER1", nil, nil); err != nil {
		t.Fatalf("PreRegister: %v", err)
	}
	if _, err := repo.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "TestVendor", OUI: "001349", ProductClass: "NR7101", SerialNumber: "SER1"}, []string{"2 PERIODIC"}); err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE devices SET online_status = 'ONLINE', last_inform_at = now() - interval '10 minutes' WHERE oui_serial = 'LIVENESS-01'`); err != nil {
		t.Fatalf("set stale device: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, online_status, last_inform_at, first_seen_at, last_updated_at) VALUES ($1, $2, $3, $4, $5, $6, 'OFFLINE', now() - interval '3 hours', now(), now())`, uuid.MustParse("11111111-1111-4111-8111-111111111111").String(), "LIVENESS-02", "TestVendor", "001349", "NR7101", "SER2"); err != nil {
		t.Fatalf("prepare unreachable row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, online_status, last_inform_at, first_seen_at, last_updated_at) VALUES ($1, $2, $3, $4, $5, $6, 'ONLINE', now() - interval '30 minutes', now(), now())`, uuid.MustParse("22222222-2222-4222-8222-222222222222").String(), "LIVENESS-03", "TestVendor", "001349", "NR7101", "SER3"); err != nil {
		t.Fatalf("prepare fresh row: %v", err)
	}

	if _, _, err := repo.RefreshLiveness(ctx, 5*time.Minute, 90*time.Minute); err != nil {
		t.Fatalf("RefreshLiveness: %v", err)
	}
	for _, tc := range []struct {
		ouiSerial string
		want      string
	}{
		{"LIVENESS-01", "OFFLINE"},
		{"LIVENESS-02", "UNREACHABLE"},
		{"LIVENESS-03", "ONLINE"},
	} {
		device, err := repo.GetByOUIserial(ctx, tc.ouiSerial)
		if err != nil {
			t.Fatalf("GetByOUIserial(%s): %v", tc.ouiSerial, err)
		}
		if device.OnlineStatus != tc.want {
			t.Fatalf("device %s online_status = %q, want %q", tc.ouiSerial, device.OnlineStatus, tc.want)
		}
	}
}

func vendorInform(t *testing.T, manufacturer, oui, productClass, serial string) string {
	s := periodicInform(t)
	s = strings.Replace(s, "<Manufacturer>Zyxel</Manufacturer>", "<Manufacturer>"+manufacturer+"</Manufacturer>", 1)
	s = strings.Replace(s, "<OUI>001349</OUI>", "<OUI>"+oui+"</OUI>", 1)
	s = strings.Replace(s, "<ProductClass>NR5103</ProductClass>", "<ProductClass>"+productClass+"</ProductClass>", 1)
	s = strings.Replace(s, "<SerialNumber>S230Q12345678</SerialNumber>", "<SerialNumber>"+serial+"</SerialNumber>", 1)
	return s
}

func newTestGateway(t *testing.T) (*handler, *httptest.Server, context.Context) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set; skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	h := &handler{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		auth:          auth.DigestAuthenticator{Username: cpeUser, Password: cpePass},
		devices:       devices.NewRepository(db),
		sessions:      sessions.NewRepository(db),
		jobs:          jobs.NewRepository(db),
		params:        parameters.NewRepository(db),
		auditor:       observability.NewAuditor(db),
		metrics:       observability.NewMetrics("acs-test"),
		policies:      policy.NewRepository(db),
		templates:     templates.NewRepository(db),
		ipLimiter:     ratelimit.New(100000, 100000, time.Minute),
		deviceLimiter: ratelimit.New(100000, 100000, time.Minute),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleCWMP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv, ctx
}

func TestIntegration_CPEVendorProfiles(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	profiles := []struct{ manufacturer, oui, productClass, serial string }{
		{"Huawei", "00E0FC", "5G CPE Pro", "HW0001"},
		{"Nokia", "0005E4", "FastMile 5G", "NK0001"},
		{"Teltonika", "001E42", "RUTX50", "TK0001"},
		{"Zyxel", "001349", "NR7101", "ZX0001"},
	}
	for _, p := range profiles {
		cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
		if code, body := cpe.post(vendorInform(t, p.manufacturer, p.oui, p.productClass, p.serial)); code != 200 || !strings.Contains(body, "InformResponse") {
			t.Fatalf("%s Inform: %d %s", p.manufacturer, code, body)
		}
		cpe.post("")
	}
	list, err := h.devices.List(ctx, devices.ListParams{})
	if err != nil || list.Total != len(profiles) {
		t.Fatalf("devices registered = %d, want %d (%v)", list.Total, len(profiles), err)
	}
	seen := map[string]bool{}
	for _, d := range list.Items {
		seen[d.Manufacturer] = true
	}
	for _, p := range profiles {
		if !seen[p.manufacturer] {
			t.Errorf("%s not registered", p.manufacturer)
		}
	}
}

func TestIntegration_CPETransferComplete(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(periodicInform(t))
	cpe.post("")
	list, _ := h.devices.List(ctx, devices.ListParams{})
	device := list.Items[0]

	job, err := h.jobs.Create(ctx, device.ID, jobs.TypeFirmwareDownload, jobs.FirmwareDownloadPayload{
		FirmwareImageID: "img", FileType: "1 Firmware Upgrade Image", URL: "http://acs.test/fw.bin", FileSize: 1234, TargetFilename: "fw.bin",
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	cpe.cookie = nil
	cpe.post(periodicInform(t))
	code, body := cpe.post("")
	if code != 200 || !strings.Contains(body, "<cwmp:Download>") || !strings.Contains(body, job.CommandKey) {
		t.Fatalf("expected Download RPC, got %d %s", code, body)
	}
	cpe.post(fixture(t, "download_response.xml"))
	j, _ := h.jobs.ByID(ctx, job.ID)
	if j.Status != jobs.StatusAwaitingTransferComplete {
		t.Fatalf("after DownloadResponse status = %s, want AWAITING_TRANSFER_COMPLETE", j.Status)
	}

	tc := strings.Replace(fixture(t, "transfer_complete.xml"), "fw_20260804_test0001", job.CommandKey, 1)
	cpe.cookie = nil
	cpe.post(periodicInform(t))
	if code, body = cpe.post(tc); code != 200 || !strings.Contains(body, "TransferCompleteResponse") {
		t.Fatalf("TransferComplete: %d %s", code, body)
	}
	j, _ = h.jobs.ByID(ctx, job.ID)
	if j.Status != jobs.StatusSuccess {
		t.Fatalf("after TransferComplete status = %s, want SUCCESS", j.Status)
	}
	jobsBefore, _ := h.jobs.List(ctx, device.ID, nil, false)
	cpe.post(tc)
	jobsAfter, _ := h.jobs.List(ctx, device.ID, nil, false)
	if len(jobsAfter) != len(jobsBefore) {
		t.Errorf("duplicate TransferComplete created %d extra job(s)", len(jobsAfter)-len(jobsBefore))
	}
	tcFault := strings.Replace(fixture(t, "transfer_complete_fault.xml"), "fw_20260804_test0002", job.CommandKey, 1)
	cpe.post(tcFault)
	j, _ = h.jobs.ByID(ctx, job.ID)
	if j.Status != jobs.StatusSuccess {
		t.Errorf("stale fault after success changed status to %s", j.Status)
	}
	if code, _ = cpe.post(fixture(t, "transfer_complete.xml")); code != 200 {
		t.Errorf("TransferComplete with unknown command key: %d, want 200 ack", code)
	}
}

func TestIntegration_CPEMalformed(t *testing.T) {
	_, srv, _ := newTestGateway(t)
	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(periodicInform(t))
	if code, _ := cpe.post("<soap-env:Envelope><unclosed>"); code != 400 {
		t.Errorf("malformed XML: %d, want 400", code)
	}
	cpe.cookie = nil
	if code, _ := cpe.post(""); code != 200 {
		t.Errorf("session-less empty POST: %d, want 200", code)
	}
}

func TestIntegration_CPELoad(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ACS_TEST_LOAD_DEVICES"))
	if n <= 0 {
		t.Skip("ACS_TEST_LOAD_DEVICES not set; skipping load mode")
	}
	h, srv, ctx := newTestGateway(t)
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
			serial := fmt.Sprintf("LOAD%06d", i)
			if code, _ := cpe.post(vendorInform(t, "Zyxel", "001349", "NR7101", serial)); code != 200 {
				errs <- fmt.Errorf("device %d Inform: %d", i, code)
				return
			}
			cpe.post("")
		}(i)
	}
	wg.Wait()
	close(errs)
	failed := 0
	for range errs {
		failed++
	}
	elapsed := time.Since(start)
	list, _ := h.devices.List(ctx, devices.ListParams{})
	t.Logf("load: %d devices, %d failed, %.1f sessions/s, %d registered", n, failed, float64(n)/elapsed.Seconds(), list.Total)
	if failed > 0 || list.Total != n {
		t.Errorf("load run: %d failures, %d/%d registered", failed, list.Total, n)
	}
}
