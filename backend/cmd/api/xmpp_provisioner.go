// Zero-touch XMPP provisioning driver (TR-069 Annex K). The API call
// (xmpp_handlers.go) creates the CPE's XMPP account and a
// device_xmpp_provisioning row; this background loop then walks the row
// through internal/xmppprov's states, one queued CWMP job at a time, until
// the CPE reports a ConnReqJabberID. Each step only creates a job or reads
// the outcome of the job it is waiting on, so the loop is safe to restart
// at any moment and never repeats an AddObject.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"acs/internal/credentials"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/xmpp"
	"acs/internal/xmppprov"
)

const (
	xmppProvPollInterval = 5 * time.Second
	xmppProvBatch        = 50
	// Discovery of a just-created instance occasionally comes back empty on
	// CPEs that populate the object asynchronously; a couple of retries
	// cover that without looping forever.
	xmppProvDiscoverAttempts = 3
	// The CPE needs a moment after BIND to resolve, connect, authenticate
	// and bind; each VERIFY read also waits for its next CWMP session.
	xmppProvVerifyAttempts   = 8
	xmppProvVerifyRetryDelay = 20 * time.Second
	xmppProvCreatedBy        = "system:xmpp-provision"
)

// Narrow views of the repositories, so transitions are unit-testable.
type xmppProvStore interface {
	ListActive(ctx context.Context, limit int) ([]xmppprov.Provisioning, error)
	Save(ctx context.Context, p *xmppprov.Provisioning, fromState string) error
}

type xmppProvJobs interface {
	Create(ctx context.Context, deviceID, jobType string, payload any, createdBy string) (*jobs.Job, error)
	ByID(ctx context.Context, id string) (*jobs.Job, error)
}

type xmppProvParams interface {
	GetNames(ctx context.Context, deviceID string) (*parameters.DiscoveredNames, error)
	Get(ctx context.Context, deviceID string) (map[string]parameters.CachedValue, error)
}

type xmppProvCredentials interface {
	ByID(ctx context.Context, id string) (*credentials.Credential, error)
	Activate(ctx context.Context, id string) (*credentials.Credential, error)
}

type xmppProvAuditor interface {
	Record(ctx context.Context, actor, deviceID, action string, details map[string]any) error
}

type xmppProvisioner struct {
	logger      *slog.Logger
	repo        xmppProvStore
	jobs        xmppProvJobs
	params      xmppProvParams
	credentials xmppProvCredentials
	auditor     xmppProvAuditor
	now         func() time.Time // nil = time.Now
}

func (x *xmppProvisioner) clock() time.Time {
	if x.now != nil {
		return x.now()
	}
	return time.Now()
}

func (x *xmppProvisioner) Run(ctx context.Context) {
	ticker := time.NewTicker(xmppProvPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := x.repo.ListActive(ctx, xmppProvBatch)
			if err != nil {
				x.logger.Error("failed to list active xmpp provisioning", "err", err)
				continue
			}
			for i := range rows {
				if err := x.step(ctx, &rows[i]); err != nil && !errors.Is(err, xmppprov.ErrConflict) {
					x.logger.Error("xmpp provisioning step failed", "err", err, "device_id", rows[i].DeviceID, "state", rows[i].State)
				}
			}
		}
	}
}

// jobPending reports statuses after which a job may still run.
func jobPending(status string) bool {
	switch status {
	case jobs.StatusQueued, jobs.StatusRPCSent, jobs.StatusInProgress, jobs.StatusAwaitingTransferComplete:
		return true
	}
	return false
}

func jobFailure(job *jobs.Job) string {
	msg := job.Status
	if job.FaultCode != nil && *job.FaultCode != "" {
		msg += " " + *job.FaultCode
	}
	if job.FaultString != nil && *job.FaultString != "" {
		msg += ": " + *job.FaultString
	}
	return msg
}

// await returns the job the row is waiting on once it has finished,
// creating it first when the row has none. done=false means "nothing to do
// this tick".
func (x *xmppProvisioner) await(ctx context.Context, p *xmppprov.Provisioning, create func() (*jobs.Job, error)) (*jobs.Job, bool, error) {
	if p.JobID == "" {
		job, err := create()
		if err != nil {
			return nil, false, err
		}
		from := p.State
		p.JobID = job.ID
		if err := x.repo.Save(ctx, p, from); err != nil {
			return nil, false, err
		}
		x.logger.Info("xmpp provisioning queued job", "device_id", p.DeviceID, "state", p.State, "job_type", job.Type, "command_key", job.CommandKey)
		return nil, false, nil
	}
	job, err := x.jobs.ByID(ctx, p.JobID)
	if err != nil {
		// A job that vanished (retention) cannot finish; treat as failed.
		return &jobs.Job{ID: p.JobID, Status: jobs.StatusFailed, FaultString: ptr("job no longer exists")}, true, nil
	}
	if jobPending(job.Status) {
		return nil, false, nil
	}
	return job, true, nil
}

func ptr(s string) *string { return &s }

// advance moves the row to next, clearing per-state bookkeeping, and audits
// the transition (never with secret values).
func (x *xmppProvisioner) advance(ctx context.Context, p *xmppprov.Provisioning, next string) error {
	from := p.State
	p.State, p.JobID, p.Attempts = next, "", 0
	if next != xmppprov.StateFailed {
		p.LastError = ""
	}
	if xmppprov.Terminal(next) {
		t := x.clock().UTC()
		p.CompletedAt = &t
	}
	if err := x.repo.Save(ctx, p, from); err != nil {
		return err
	}
	detail := map[string]any{"from": from, "to": next, "connection_path": p.ConnectionPath, "server_path": p.ServerPath, "credential_id": p.CredentialID}
	if next == xmppprov.StateFailed {
		detail["error"] = p.LastError
	}
	if next == xmppprov.StateDone {
		detail["jabber_id"] = p.JabberID
	}
	if x.auditor != nil {
		if err := x.auditor.Record(ctx, xmppProvCreatedBy, p.DeviceID, "XMPPProvisioning", detail); err != nil {
			x.logger.Error("failed to write audit record", "err", err)
		}
	}
	x.logger.Info("xmpp provisioning advanced", "device_id", p.DeviceID, "from", from, "to", next, "error", p.LastError)
	return nil
}

func (x *xmppProvisioner) fail(ctx context.Context, p *xmppprov.Provisioning, format string, args ...any) error {
	p.LastError = fmt.Sprintf(format, args...)
	return x.advance(ctx, p, xmppprov.StateFailed)
}

// retry records a failed attempt in the current state and fails the run
// once attempts are exhausted.
func (x *xmppProvisioner) retry(ctx context.Context, p *xmppprov.Provisioning, max int, reason string) error {
	if p.Attempts+1 >= max {
		return x.fail(ctx, p, "%s (after %d attempts)", reason, p.Attempts+1)
	}
	from := p.State
	p.Attempts++
	p.JobID = ""
	p.LastError = reason
	return x.repo.Save(ctx, p, from)
}

func (x *xmppProvisioner) names(ctx context.Context, deviceID string) (map[string]bool, error) {
	d, err := x.params.GetNames(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return map[string]bool{}, nil
	}
	return d.Names, nil
}

func (x *xmppProvisioner) step(ctx context.Context, p *xmppprov.Provisioning) error {
	switch p.State {
	case xmppprov.StateAddConnection:
		return x.stepAddObject(ctx, p, xmppprov.ConnectionTable, func(path string) string {
			p.ConnectionPath = path
			return xmppprov.StateDiscoverConnection
		})
	case xmppprov.StateDiscoverConnection:
		return x.stepDiscover(ctx, p, p.ConnectionPath, func(names map[string]bool) (string, error) {
			if _, ok := names[p.ConnectionPath+"Username"]; !ok {
				return "", errNotYetDiscovered
			}
			if srv := xmppprov.FirstInstance(names, p.ConnectionPath+"Server."); srv != "" {
				p.ServerPath = srv // the CPE created one along with the connection
				return xmppprov.StateConfigure, nil
			}
			if _, ok := names[p.ConnectionPath+"Server."]; ok {
				return xmppprov.StateAddServer, nil
			}
			return "", fmt.Errorf("%sServer. is not exposed; the ACS cannot tell the CPE where its XMPP server is", p.ConnectionPath)
		})
	case xmppprov.StateAddServer:
		return x.stepAddObject(ctx, p, p.ConnectionPath+"Server.", func(path string) string {
			p.ServerPath = path
			return xmppprov.StateDiscoverServer
		})
	case xmppprov.StateDiscoverServer:
		return x.stepDiscover(ctx, p, p.ServerPath, func(names map[string]bool) (string, error) {
			if _, ok := names[p.ServerPath+"ServerAddress"]; !ok {
				return "", errNotYetDiscovered
			}
			return xmppprov.StateConfigure, nil
		})
	case xmppprov.StateConfigure:
		return x.stepSet(ctx, p, xmppprov.StateBind, func(names map[string]bool) ([]jobs.ParameterWrite, error) {
			if p.CredentialID == "" {
				return nil, errors.New("the XMPP credential was deleted")
			}
			cred, err := x.credentials.ByID(ctx, p.CredentialID)
			if err != nil {
				return nil, fmt.Errorf("load XMPP credential: %w", err)
			}
			return xmppprov.ConfigureWrites(names, p, cred.Username, cred.Password)
		})
	case xmppprov.StateBind:
		return x.stepSet(ctx, p, xmppprov.StateVerify, func(names map[string]bool) ([]jobs.ParameterWrite, error) {
			return xmppprov.BindWrites(names, p)
		})
	case xmppprov.StateVerify:
		return x.stepVerify(ctx, p)
	}
	return nil
}

var errNotYetDiscovered = errors.New("the new instance's parameters were not in the discovery result")

func (x *xmppProvisioner) stepAddObject(ctx context.Context, p *xmppprov.Provisioning, object string, onCreated func(path string) string) error {
	job, done, err := x.await(ctx, p, func() (*jobs.Job, error) {
		return x.jobs.Create(ctx, p.DeviceID, jobs.TypeAddObject, jobs.AddObjectPayload{ObjectPath: object}, xmppProvCreatedBy)
	})
	if err != nil || !done {
		return err
	}
	if job.Status != jobs.StatusSuccess {
		return x.fail(ctx, p, "AddObject %s failed: %s", object, jobFailure(job))
	}
	var detail struct {
		InstanceNumber int `json:"instance_number"`
	}
	if err := json.Unmarshal(job.ResultDetail, &detail); err != nil || detail.InstanceNumber <= 0 {
		return x.fail(ctx, p, "AddObject %s returned no instance number", object)
	}
	return x.advance(ctx, p, onCreated(xmppprov.InstancePath(object, detail.InstanceNumber)))
}

func (x *xmppProvisioner) stepDiscover(ctx context.Context, p *xmppprov.Provisioning, root string, next func(map[string]bool) (string, error)) error {
	job, done, err := x.await(ctx, p, func() (*jobs.Job, error) {
		return x.jobs.Create(ctx, p.DeviceID, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{Root: root, Merge: true}, xmppProvCreatedBy)
	})
	if err != nil || !done {
		return err
	}
	if job.Status != jobs.StatusSuccess {
		return x.retry(ctx, p, xmppProvDiscoverAttempts, "discovery of "+root+" failed: "+jobFailure(job))
	}
	names, err := x.names(ctx, p.DeviceID)
	if err != nil {
		return err
	}
	state, err := next(names)
	if errors.Is(err, errNotYetDiscovered) {
		return x.retry(ctx, p, xmppProvDiscoverAttempts, err.Error()+": "+root)
	}
	if err != nil {
		return x.fail(ctx, p, "%v", err)
	}
	return x.advance(ctx, p, state)
}

func (x *xmppProvisioner) stepSet(ctx context.Context, p *xmppprov.Provisioning, next string, build func(map[string]bool) ([]jobs.ParameterWrite, error)) error {
	var buildErr error
	job, done, err := x.await(ctx, p, func() (*jobs.Job, error) {
		names, err := x.names(ctx, p.DeviceID)
		if err != nil {
			return nil, err
		}
		writes, err := build(names)
		if err != nil {
			buildErr = err
			return nil, err
		}
		// Known issue S1: like credential rotation, the SET payload (and,
		// after success, the parameter cache until the confirmation read
		// returns the CPE's masked value) holds the password in plaintext.
		// Provisioning owns the XMPP Connection Request settings, which every
		// other write path is refused (parameters.ProtectedReason).
		return x.jobs.Create(jobs.AllowProtectedWrites(ctx, "XMPP provisioning"), p.DeviceID, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: writes}, xmppProvCreatedBy)
	})
	if buildErr != nil {
		return x.fail(ctx, p, "%s: %v", p.State, buildErr)
	}
	if err != nil || !done {
		return err
	}
	if job.Status != jobs.StatusSuccess {
		return x.fail(ctx, p, "SetParameterValues (%s) failed: %s", p.State, jobFailure(job))
	}
	return x.advance(ctx, p, next)
}

func (x *xmppProvisioner) stepVerify(ctx context.Context, p *xmppprov.Provisioning) error {
	if p.JobID == "" && p.Attempts > 0 && x.clock().Sub(p.UpdatedAt) < xmppProvVerifyRetryDelay {
		return nil // give the CPE time to log in before reading again
	}
	job, done, err := x.await(ctx, p, func() (*jobs.Job, error) {
		names, err := x.names(ctx, p.DeviceID)
		if err != nil {
			return nil, err
		}
		return x.jobs.Create(ctx, p.DeviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: xmppprov.VerifyPaths(names, p)}, xmppProvCreatedBy)
	})
	if err != nil || !done {
		return err
	}
	cached, err := x.params.Get(ctx, p.DeviceID)
	if err != nil {
		return err
	}
	p.JabberID = strings.TrimSpace(cached[xmppprov.ManagementServer+"ConnReqJabberID"].Value)
	p.ConnectionStatus = cached[p.ConnectionPath+"Status"].Value

	wantBare := ""
	if p.CredentialID != "" {
		if cred, err := x.credentials.ByID(ctx, p.CredentialID); err == nil {
			wantBare = cred.Username + "@" + p.Domain
		}
	}
	switch {
	case job.Status != jobs.StatusSuccess:
		return x.retry(ctx, p, xmppProvVerifyAttempts, "verification read failed: "+jobFailure(job))
	case p.JabberID == "":
		return x.retry(ctx, p, xmppProvVerifyAttempts, fmt.Sprintf("CPE has not reported a ConnReqJabberID yet (XMPP Connection Status %q)", p.ConnectionStatus))
	case xmpp.ValidateFullJID(p.JabberID) != nil:
		return x.retry(ctx, p, xmppProvVerifyAttempts, "CPE reported a ConnReqJabberID that is not a full JID")
	case wantBare != "" && !strings.EqualFold(xmpp.Bare(p.JabberID), wantBare):
		return x.retry(ctx, p, xmppProvVerifyAttempts, "CPE's ConnReqJabberID does not belong to the provisioned account")
	}
	// The CPE logged in with the provisioned account: that is the
	// activation proof for the XMPP credential.
	if cred, err := x.credentials.ByID(ctx, p.CredentialID); err == nil && cred.Status == credentials.StatusPending {
		if _, err := x.credentials.Activate(ctx, p.CredentialID); err != nil {
			return fmt.Errorf("activate XMPP credential: %w", err)
		}
	}
	return x.advance(ctx, p, xmppprov.StateDone)
}
