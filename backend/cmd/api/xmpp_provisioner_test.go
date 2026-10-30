package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"acs/internal/credentials"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/xmppprov"
)

// --- in-memory fakes for the provisioner's narrow interfaces ---

type fakeProvStore struct {
	rows map[string]xmppprov.Provisioning
}

func (f *fakeProvStore) ListActive(context.Context, int) ([]xmppprov.Provisioning, error) {
	var out []xmppprov.Provisioning
	for _, p := range f.rows {
		if !xmppprov.Terminal(p.State) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProvStore) Save(_ context.Context, p *xmppprov.Provisioning, from string) error {
	if f.rows[p.DeviceID].State != from {
		return xmppprov.ErrConflict
	}
	p.UpdatedAt = time.Now()
	f.rows[p.DeviceID] = *p
	return nil
}

type fakeProvJobs struct{ jobs map[string]*jobs.Job }

func (f *fakeProvJobs) Create(_ context.Context, deviceID, jobType string, payload any, createdBy string) (*jobs.Job, error) {
	raw, _ := json.Marshal(payload)
	j := &jobs.Job{ID: fmt.Sprintf("job-%d", len(f.jobs)+1), DeviceID: deviceID, Type: jobType, Status: jobs.StatusQueued, Payload: raw, CreatedBy: createdBy}
	f.jobs[j.ID] = j
	return j, nil
}

func (f *fakeProvJobs) ByID(_ context.Context, id string) (*jobs.Job, error) {
	if j, ok := f.jobs[id]; ok {
		return j, nil
	}
	return nil, errors.New("not found")
}

type fakeProvParams struct {
	names map[string]bool
	cache map[string]parameters.CachedValue
}

func (f *fakeProvParams) GetNames(context.Context, string) (*parameters.DiscoveredNames, error) {
	return &parameters.DiscoveredNames{Names: f.names}, nil
}

func (f *fakeProvParams) Get(context.Context, string) (map[string]parameters.CachedValue, error) {
	return f.cache, nil
}

type fakeProvCreds struct {
	creds map[string]*credentials.Credential
}

func (f *fakeProvCreds) ByID(_ context.Context, id string) (*credentials.Credential, error) {
	if c, ok := f.creds[id]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, credentials.ErrNotFound
}

func (f *fakeProvCreds) Activate(_ context.Context, id string) (*credentials.Credential, error) {
	f.creds[id].Status = credentials.StatusActive
	return f.creds[id], nil
}

type provHarness struct {
	t      *testing.T
	x      *xmppProvisioner
	store  *fakeProvStore
	jobs   *fakeProvJobs
	params *fakeProvParams
	creds  *fakeProvCreds
	now    time.Time
}

// fastMileNames is the slice of the discovered Nokia FastMile 5G Gateway 2
// (1.2501.00.0798) tree XMPP provisioning starts from: a writable, empty
// Device.XMPP.Connection. table and the ManagementServer knobs.
func fastMileNames() map[string]bool {
	return map[string]bool{
		"Device.XMPP.":            false,
		"Device.XMPP.Connection.": true,
		"Device.XMPP.X_ALU-COM_CertificateVerificationRequired": true,
		"Device.XMPP.X_ALU-COM_BypassHostnameCheck":             true,
		"Device.ManagementServer.ConnReqAllowedJabberIDs":       true,
		"Device.ManagementServer.ConnReqJabberID":               false,
		"Device.ManagementServer.ConnReqXMPPConnection":         true,
		"Device.ManagementServer.SupportedConnReqMethods":       false,
		"Device.ManagementServer.X_ALU_COM_XMPP_Enable":         true,
	}
}

func newProvHarness(t *testing.T, state string) *provHarness {
	h := &provHarness{
		t: t, now: time.Now(),
		store:  &fakeProvStore{rows: map[string]xmppprov.Provisioning{}},
		jobs:   &fakeProvJobs{jobs: map[string]*jobs.Job{}},
		params: &fakeProvParams{names: fastMileNames(), cache: map[string]parameters.CachedValue{}},
		creds: &fakeProvCreds{creds: map[string]*credentials.Credential{
			"cred-1": {ID: "cred-1", CredentialType: credentials.TypeXMPP, Username: "xmpp-0011223344556677", Password: "secret-xmpp-password", Status: credentials.StatusPending},
		}},
	}
	h.x = &xmppProvisioner{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), repo: h.store, jobs: h.jobs,
		params: h.params, credentials: h.creds, now: func() time.Time { return h.now },
	}
	h.store.rows["dev-1"] = xmppprov.Provisioning{
		DeviceID: "dev-1", State: state, CredentialID: "cred-1", Domain: "xmpp.acs.lab", Resource: "cwmp",
		ServerAddress: "13.245.18.190", ServerPort: 5222, KeepAliveInterval: 60, ACSJID: "acs@xmpp.acs.lab",
	}
	return h
}

func (h *provHarness) row() xmppprov.Provisioning { return h.store.rows["dev-1"] }

func (h *provHarness) step() {
	h.t.Helper()
	p := h.row()
	if err := h.x.step(context.Background(), &p); err != nil {
		h.t.Fatalf("step in %s: %v", p.State, err)
	}
}

// pendingJob returns the job the row waits on.
func (h *provHarness) pendingJob() *jobs.Job {
	h.t.Helper()
	id := h.row().JobID
	if id == "" {
		h.t.Fatalf("state %s has no job", h.row().State)
	}
	return h.jobs.jobs[id]
}

func (h *provHarness) finish(status string, detail any) {
	j := h.pendingJob()
	j.Status = status
	if detail != nil {
		j.ResultDetail, _ = json.Marshal(detail)
	}
}

func (h *provHarness) expectState(want string) {
	h.t.Helper()
	if got := h.row(); got.State != want {
		h.t.Fatalf("state = %s (last_error %q), want %s", got.State, got.LastError, want)
	}
}

func writesOf(t *testing.T, j *jobs.Job) map[string]string {
	t.Helper()
	var p jobs.SetParameterPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, w := range p.Parameters {
		out[w.Name] = w.Value
	}
	return out
}

// TestXMPPProvisioningHappyPath walks a FastMile-shaped device through the
// whole state machine: AddObject connection, subtree discovery, AddObject
// server, discovery, configure, bind, then VERIFY until the CPE reports a
// JID — which activates the XMPP credential.
func TestXMPPProvisioningHappyPath(t *testing.T) {
	h := newProvHarness(t, xmppprov.StateAddConnection)
	const conn = "Device.XMPP.Connection.1."

	h.step()
	if j := h.pendingJob(); j.Type != jobs.TypeAddObject || !strings.Contains(string(j.Payload), `"Device.XMPP.Connection."`) {
		t.Fatalf("first job = %s %s", j.Type, j.Payload)
	}
	h.step() // still queued: nothing happens, no second AddObject
	if len(h.jobs.jobs) != 1 {
		t.Fatalf("a pending AddObject was repeated: %d jobs", len(h.jobs.jobs))
	}
	h.finish(jobs.StatusSuccess, map[string]any{"instance_number": 1})
	h.step()
	h.expectState(xmppprov.StateDiscoverConnection)
	if h.row().ConnectionPath != conn {
		t.Fatalf("connection path = %q", h.row().ConnectionPath)
	}

	h.step()
	var disc jobs.ParameterDiscoveryPayload
	_ = json.Unmarshal(h.pendingJob().Payload, &disc)
	if h.pendingJob().Type != jobs.TypeParameterDiscovery || disc.Root != conn || !disc.Merge || disc.Incremental {
		t.Fatalf("discovery job = %s %+v", h.pendingJob().Type, disc)
	}
	for _, leaf := range []string{"Enable", "Username", "Password", "Domain", "Resource", "UseTLS", "KeepAliveInterval", "ServerConnectAlgorithm", "Server."} {
		h.params.names[conn+leaf] = true
	}
	h.params.names[conn+"Status"] = false
	h.params.names[conn+"JabberID"] = false
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateAddServer)

	h.step()
	if !strings.Contains(string(h.pendingJob().Payload), `"`+conn+`Server."`) {
		t.Fatalf("server AddObject payload = %s", h.pendingJob().Payload)
	}
	h.finish(jobs.StatusSuccess, map[string]any{"instance_number": 1})
	h.step()
	h.expectState(xmppprov.StateDiscoverServer)
	srv := conn + "Server.1."
	h.step()
	for _, leaf := range []string{"Enable", "ServerAddress", "Port"} {
		h.params.names[srv+leaf] = true
	}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateConfigure)

	h.step()
	cfg := writesOf(t, h.pendingJob())
	want := map[string]string{
		conn + "Username": "xmpp-0011223344556677", conn + "Password": "secret-xmpp-password", conn + "Domain": "xmpp.acs.lab",
		conn + "Resource": "cwmp", conn + "UseTLS": "true", conn + "KeepAliveInterval": "60", conn + "Enable": "true",
		conn + "ServerConnectAlgorithm": "ServerTable", srv + "ServerAddress": "13.245.18.190", srv + "Port": "5222", srv + "Enable": "true",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("configure %s = %q, want %q", k, cfg[k], v)
		}
	}
	if _, relaxed := cfg["Device.XMPP.X_ALU-COM_CertificateVerificationRequired"]; relaxed {
		t.Error("certificate verification was relaxed without the operator asking")
	}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateBind)

	h.step()
	bind := writesOf(t, h.pendingJob())
	if bind["Device.ManagementServer.ConnReqXMPPConnection"] != "Device.XMPP.Connection.1" ||
		bind["Device.ManagementServer.ConnReqAllowedJabberIDs"] != "acs@xmpp.acs.lab" ||
		bind["Device.ManagementServer.X_ALU_COM_XMPP_Enable"] != "true" {
		t.Fatalf("bind writes = %v", bind)
	}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateVerify)

	// First read: the CPE has not logged in yet.
	h.step()
	var get jobs.GetParameterPayload
	_ = json.Unmarshal(h.pendingJob().Payload, &get)
	if strings.Join(get.Paths, ",") != "Device.ManagementServer.ConnReqJabberID,"+conn+"Status,"+conn+"JabberID" {
		t.Fatalf("verify paths = %v", get.Paths)
	}
	h.params.cache[conn+"Status"] = parameters.CachedValue{Value: "Connecting"}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateVerify)
	if r := h.row(); r.Attempts != 1 || r.JobID != "" || !strings.Contains(r.LastError, "Connecting") {
		t.Fatalf("after empty read: %+v", r)
	}
	h.step() // inside the retry delay: no new read yet
	if h.row().JobID != "" {
		t.Fatal("re-read before the retry delay")
	}
	h.now = h.now.Add(xmppProvVerifyRetryDelay + time.Second)
	h.step()
	h.params.cache["Device.ManagementServer.ConnReqJabberID"] = parameters.CachedValue{Value: "xmpp-0011223344556677@xmpp.acs.lab/cwmp"}
	h.params.cache[conn+"Status"] = parameters.CachedValue{Value: "Enabled"}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateDone)
	if r := h.row(); r.JabberID != "xmpp-0011223344556677@xmpp.acs.lab/cwmp" || r.CompletedAt == nil || r.ConnectionStatus != "Enabled" {
		t.Fatalf("done row = %+v", r)
	}
	if h.creds.creds["cred-1"].Status != credentials.StatusActive {
		t.Fatal("the XMPP credential was not activated")
	}
}

// TestXMPPProvisioningUsesCPECreatedServer: a CPE that creates Server.1
// together with the connection skips ADD_SERVER (no duplicate server rows).
func TestXMPPProvisioningUsesCPECreatedServer(t *testing.T) {
	h := newProvHarness(t, xmppprov.StateDiscoverConnection)
	p := h.row()
	p.ConnectionPath = "Device.XMPP.Connection.2."
	h.store.rows["dev-1"] = p
	h.step()
	for _, n := range []string{"Username", "Server.", "Server.1.", "Server.1.ServerAddress"} {
		h.params.names[p.ConnectionPath+n] = true
	}
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateConfigure)
	if h.row().ServerPath != "Device.XMPP.Connection.2.Server.1." {
		t.Fatalf("server path = %q", h.row().ServerPath)
	}
}

func TestXMPPProvisioningAddObjectFaultFails(t *testing.T) {
	h := newProvHarness(t, xmppprov.StateAddConnection)
	h.step()
	j := h.pendingJob()
	j.Status, j.FaultCode, j.FaultString = jobs.StatusFailed, ptr("9005"), ptr("Invalid parameter name")
	h.step()
	h.expectState(xmppprov.StateFailed)
	if r := h.row(); !strings.Contains(r.LastError, "9005") || r.CompletedAt == nil {
		t.Fatalf("failed row = %+v", r)
	}
}

// TestXMPPProvisioningDiscoveryRetriesThenFails: an instance whose leaves
// never show up is retried a bounded number of times.
func TestXMPPProvisioningDiscoveryRetriesThenFails(t *testing.T) {
	h := newProvHarness(t, xmppprov.StateDiscoverConnection)
	p := h.row()
	p.ConnectionPath = "Device.XMPP.Connection.1."
	h.store.rows["dev-1"] = p
	for i := 0; i < xmppProvDiscoverAttempts; i++ {
		h.step()
		h.finish(jobs.StatusSuccess, nil)
		h.step()
	}
	h.expectState(xmppprov.StateFailed)
	if len(h.jobs.jobs) != xmppProvDiscoverAttempts {
		t.Fatalf("discovery jobs = %d, want %d", len(h.jobs.jobs), xmppProvDiscoverAttempts)
	}
}

// TestXMPPProvisioningRelaxRequiresVendorKnob: the operator's explicit
// request to relax certificate checks is honoured only through a real
// writable knob; without one provisioning fails instead of silently
// leaving verification on (or guessing a path).
func TestXMPPProvisioningRelaxRequiresVendorKnob(t *testing.T) {
	for _, haveKnob := range []bool{true, false} {
		h := newProvHarness(t, xmppprov.StateConfigure)
		p := h.row()
		p.ConnectionPath, p.ServerPath, p.RelaxCertVerification, p.BypassHostnameCheck = "Device.XMPP.Connection.1.", "Device.XMPP.Connection.1.Server.1.", true, true
		h.store.rows["dev-1"] = p
		for _, n := range []string{"Username", "Password", "Domain", "Server.1.ServerAddress"} {
			h.params.names[p.ConnectionPath+n] = true
		}
		if !haveKnob {
			delete(h.params.names, "Device.XMPP.X_ALU-COM_CertificateVerificationRequired")
		}
		h.step()
		if !haveKnob {
			h.expectState(xmppprov.StateFailed)
			if len(h.jobs.jobs) != 0 {
				t.Fatal("a SET was queued despite the missing knob")
			}
			continue
		}
		w := writesOf(t, h.pendingJob())
		if w["Device.XMPP.X_ALU-COM_CertificateVerificationRequired"] != "false" || w["Device.XMPP.X_ALU-COM_BypassHostnameCheck"] != "true" {
			t.Fatalf("relaxed writes = %v", w)
		}
	}
}

// TestXMPPProvisioningRejectsForeignJID: a ConnReqJabberID that is not the
// provisioned account (e.g. stale from an earlier run) is not accepted as
// the activation proof.
func TestXMPPProvisioningRejectsForeignJID(t *testing.T) {
	h := newProvHarness(t, xmppprov.StateVerify)
	p := h.row()
	p.ConnectionPath = "Device.XMPP.Connection.1."
	h.store.rows["dev-1"] = p
	h.params.cache["Device.ManagementServer.ConnReqJabberID"] = parameters.CachedValue{Value: "xmpp-old@xmpp.acs.lab/cwmp"}
	h.step()
	h.finish(jobs.StatusSuccess, nil)
	h.step()
	h.expectState(xmppprov.StateVerify)
	if h.creds.creds["cred-1"].Status != credentials.StatusPending || !strings.Contains(h.row().LastError, "does not belong") {
		t.Fatalf("row = %+v", h.row())
	}
}
