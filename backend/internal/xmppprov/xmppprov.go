// Package xmppprov persists and plans zero-touch XMPP provisioning: driving
// a CPE over TR-069 until it holds an XMPP session to the ACS's server and
// reports a ConnReqJabberID, so TR-069 Annex K Connection Requests reach it
// through CGNAT (migration 0081; driver in cmd/api/xmpp_provisioner.go).
//
// It is a state machine rather than one SetParameterValues batch because
// the CPE decides the instance numbers (AddObject's response) and only a
// GetParameterNames after the AddObject proves which leaves of the new
// instance exist and are writable on that firmware:
//
//	ADD_CONNECTION      AddObject Device.XMPP.Connection.
//	DISCOVER_CONNECTION GetParameterNames on the new instance (subtree merge)
//	ADD_SERVER          AddObject <conn>.Server. (skipped if the CPE made one)
//	DISCOVER_SERVER     GetParameterNames on the new Server instance
//	CONFIGURE           SPV: account, domain, resource, TLS, keepalive, server
//	BIND                SPV: ManagementServer.ConnReqXMPPConnection and
//	                    ConnReqAllowedJabberIDs (+ vendor XMPP enable)
//	VERIFY              GPV: ConnReqJabberID and Connection Status, repeated
//	                    until the CPE has logged in
//	DONE / FAILED
//
// Every step waits on one persisted job id, so an API restart resumes
// exactly where it stopped and never repeats an AddObject.
package xmppprov

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"acs/internal/jobs"
)

// States (device_xmpp_provisioning.state).
const (
	StateAddConnection      = "ADD_CONNECTION"
	StateDiscoverConnection = "DISCOVER_CONNECTION"
	StateAddServer          = "ADD_SERVER"
	StateDiscoverServer     = "DISCOVER_SERVER"
	StateConfigure          = "CONFIGURE"
	StateBind               = "BIND"
	StateVerify             = "VERIFY"
	StateDone               = "DONE"
	StateFailed             = "FAILED"
)

// Terminal reports whether a state is final.
func Terminal(state string) bool { return state == StateDone || state == StateFailed }

// Data-model paths (TR-181 Device:2 only — the XMPP object does not exist in
// TR-098).
const (
	ConnectionTable  = "Device.XMPP.Connection."
	ManagementServer = "Device.ManagementServer."
	xmppRoot         = "Device.XMPP."
)

var (
	ErrNotFound = errors.New("no xmpp provisioning for device")
	// ErrActive means a provisioning run is already in progress.
	ErrActive = errors.New("xmpp provisioning already in progress")
	// ErrConflict means the row moved on underneath a Save (another
	// replica stepped it first); the caller simply re-reads next tick.
	ErrConflict = errors.New("xmpp provisioning state changed concurrently")
)

// Provisioning is one device_xmpp_provisioning row. It carries no secret:
// CredentialID points at the encrypted device_credentials row.
type Provisioning struct {
	DeviceID              string
	State                 string
	CredentialID          string
	ConnectionPath        string // e.g. "Device.XMPP.Connection.1."
	ServerPath            string // e.g. "Device.XMPP.Connection.1.Server.1."
	JobID                 string
	Attempts              int
	Domain                string
	Resource              string
	ServerAddress         string
	ServerPort            int
	KeepAliveInterval     int
	ACSJID                string
	RelaxCertVerification bool
	BypassHostnameCheck   bool
	JabberID              string
	ConnectionStatus      string
	LastError             string
	RequestedBy           string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           *time.Time
}

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const columns = `device_id, state, COALESCE(credential_id::text, ''), COALESCE(connection_path, ''),
	COALESCE(server_path, ''), COALESCE(job_id::text, ''), attempts, domain, resource, server_address,
	server_port, keepalive_interval, acs_jid, relax_cert_verification, bypass_hostname_check,
	COALESCE(jabber_id, ''), COALESCE(connection_status, ''), COALESCE(last_error, ''),
	COALESCE(requested_by, ''), created_at, updated_at, completed_at`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (*Provisioning, error) {
	var p Provisioning
	var completed sql.NullTime
	if err := s.Scan(&p.DeviceID, &p.State, &p.CredentialID, &p.ConnectionPath, &p.ServerPath, &p.JobID,
		&p.Attempts, &p.Domain, &p.Resource, &p.ServerAddress, &p.ServerPort, &p.KeepAliveInterval, &p.ACSJID,
		&p.RelaxCertVerification, &p.BypassHostnameCheck, &p.JabberID, &p.ConnectionStatus, &p.LastError,
		&p.RequestedBy, &p.CreatedAt, &p.UpdatedAt, &completed); err != nil {
		return nil, err
	}
	if completed.Valid {
		t := completed.Time
		p.CompletedAt = &t
	}
	return &p, nil
}

// Get returns the device's provisioning row.
func (r *Repository) Get(ctx context.Context, deviceID string) (*Provisioning, error) {
	p, err := scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM device_xmpp_provisioning WHERE device_id = $1`, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get xmpp provisioning: %w", err)
	}
	return p, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Start creates (or replaces a finished) provisioning row atomically:
// ErrActive when one is still running, so two concurrent API calls cannot
// both start AddObject'ing.
func (r *Repository) Start(ctx context.Context, p *Provisioning) error {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO device_xmpp_provisioning (device_id, state, credential_id, connection_path, domain, resource,
			server_address, server_port, keepalive_interval, acs_jid, relax_cert_verification, bypass_hostname_check, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (device_id) DO UPDATE SET
			state = EXCLUDED.state, credential_id = EXCLUDED.credential_id, connection_path = EXCLUDED.connection_path,
			server_path = NULL, job_id = NULL, attempts = 0, domain = EXCLUDED.domain, resource = EXCLUDED.resource,
			server_address = EXCLUDED.server_address, server_port = EXCLUDED.server_port,
			keepalive_interval = EXCLUDED.keepalive_interval, acs_jid = EXCLUDED.acs_jid,
			relax_cert_verification = EXCLUDED.relax_cert_verification, bypass_hostname_check = EXCLUDED.bypass_hostname_check,
			jabber_id = NULL, connection_status = NULL, last_error = NULL, requested_by = EXCLUDED.requested_by,
			created_at = now(), updated_at = now(), completed_at = NULL
		WHERE device_xmpp_provisioning.state IN ('DONE', 'FAILED')`,
		p.DeviceID, p.State, nullIfEmpty(p.CredentialID), nullIfEmpty(p.ConnectionPath), p.Domain, p.Resource,
		p.ServerAddress, p.ServerPort, p.KeepAliveInterval, p.ACSJID, p.RelaxCertVerification, p.BypassHostnameCheck,
		nullIfEmpty(p.RequestedBy))
	if err != nil {
		return fmt.Errorf("start xmpp provisioning: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrActive
	}
	return nil
}

// Save persists a step's result, guarded by the state it was read in.
func (r *Repository) Save(ctx context.Context, p *Provisioning, fromState string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE device_xmpp_provisioning SET state = $3, connection_path = $4, server_path = $5, job_id = $6,
			attempts = $7, jabber_id = $8, connection_status = $9, last_error = $10, completed_at = $11, updated_at = now()
		WHERE device_id = $1 AND state = $2`,
		p.DeviceID, fromState, p.State, nullIfEmpty(p.ConnectionPath), nullIfEmpty(p.ServerPath), nullIfEmpty(p.JobID),
		p.Attempts, nullIfEmpty(p.JabberID), nullIfEmpty(p.ConnectionStatus), nullIfEmpty(p.LastError), p.CompletedAt)
	if err != nil {
		return fmt.Errorf("save xmpp provisioning: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// ListActive returns non-terminal rows, least recently touched first.
func (r *Repository) ListActive(ctx context.Context, limit int) ([]Provisioning, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM device_xmpp_provisioning
		WHERE state NOT IN ('DONE', 'FAILED') ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active xmpp provisioning: %w", err)
	}
	defer rows.Close()
	var out []Provisioning
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// --- planning (pure) ---

// InstancePath renders "<object><n>." for an AddObject result.
func InstancePath(object string, instance int) string {
	return object + strconv.Itoa(instance) + "."
}

// FirstInstance returns the lowest-numbered discovered instance of a
// multi-instance object (e.g. "<conn>Server." -> "<conn>Server.1."), or "".
func FirstInstance(names map[string]bool, object string) string {
	var nums []int
	for name := range names {
		rest, ok := strings.CutPrefix(name, object)
		if !ok {
			continue
		}
		num, _, _ := strings.Cut(rest, ".")
		if n, err := strconv.Atoi(num); err == nil && n > 0 {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return ""
	}
	sort.Ints(nums)
	return InstancePath(object, nums[0])
}

// ConnectionReference is the value ManagementServer.ConnReqXMPPConnection
// takes: a path reference to the Connection instance, which TR-181 writes
// without the trailing dot ("Device.XMPP.Connection.1").
func ConnectionReference(connectionPath string) string {
	return strings.TrimSuffix(connectionPath, ".")
}

// writer accumulates SetParameterValues entries, writing only leaves the
// discovered tree proves exist and are writable.
type writer struct {
	names   map[string]bool
	writes  []jobs.ParameterWrite
	missing []string
}

func (w *writer) set(name, value, typ string, required bool) {
	writable, known := w.names[name]
	if !known || !writable {
		if required {
			w.missing = append(w.missing, name)
		}
		return
	}
	w.writes = append(w.writes, jobs.ParameterWrite{Name: name, Value: value, Type: typ})
}

func (w *writer) result() ([]jobs.ParameterWrite, error) {
	if len(w.missing) > 0 {
		return nil, fmt.Errorf("device does not expose these as writable parameters: %s", strings.Join(w.missing, ", "))
	}
	return w.writes, nil
}

// vendorParam finds a vendor-extension leaf under prefix whose name ends in
// suffix (e.g. Device.XMPP.X_ALU-COM_CertificateVerificationRequired) —
// vendors spell the prefix differently (X_ALU-COM_, X_ALU_COM_, …).
func vendorParam(names map[string]bool, prefix, suffix string) string {
	var found []string
	for name := range names {
		rest, ok := strings.CutPrefix(name, prefix)
		if ok && strings.HasPrefix(rest, "X_") && !strings.Contains(rest, ".") && strings.HasSuffix(rest, suffix) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// ConfigureWrites is the CONFIGURE step's SetParameterValues: the CPE's
// XMPP account and how to reach the server. Username/Password/Domain and
// the server address are required; the rest is written when present.
// Certificate checks are relaxed only when the operator asked (a lab
// self-signed certificate), and asking for it on a CPE without the vendor
// knob is an error rather than a silent no-op.
func ConfigureWrites(names map[string]bool, p *Provisioning, username, password string) ([]jobs.ParameterWrite, error) {
	w := &writer{names: names}
	c := p.ConnectionPath
	w.set(c+"Username", username, "xsd:string", true)
	w.set(c+"Password", password, "xsd:string", true)
	w.set(c+"Domain", p.Domain, "xsd:string", true)
	w.set(c+"Resource", p.Resource, "xsd:string", false)
	w.set(c+"UseTLS", "true", "xsd:boolean", false)
	w.set(c+"KeepAliveInterval", strconv.Itoa(p.KeepAliveInterval), "xsd:long", false)
	w.set(c+"ServerConnectAlgorithm", "ServerTable", "xsd:string", false)
	if p.ServerPath == "" {
		w.missing = append(w.missing, c+"Server.{i}.ServerAddress")
	} else {
		s := p.ServerPath
		w.set(s+"Enable", "true", "xsd:boolean", false)
		w.set(s+"ServerAddress", p.ServerAddress, "xsd:string", true)
		w.set(s+"Port", strconv.Itoa(p.ServerPort), "xsd:unsignedInt", false)
	}
	if p.RelaxCertVerification {
		if name := vendorParam(names, xmppRoot, "CertificateVerificationRequired"); name != "" {
			w.set(name, "false", "xsd:boolean", true)
		} else {
			w.missing = append(w.missing, xmppRoot+"X_*_CertificateVerificationRequired")
		}
	}
	if p.BypassHostnameCheck {
		if name := vendorParam(names, xmppRoot, "BypassHostnameCheck"); name != "" {
			w.set(name, "true", "xsd:boolean", true)
		} else {
			w.missing = append(w.missing, xmppRoot+"X_*_BypassHostnameCheck")
		}
	}
	w.set(c+"Enable", "true", "xsd:boolean", false)
	return w.result()
}

// BindWrites is the BIND step: point Connection Request at the XMPP
// connection and allow only the ACS's JID to use it.
func BindWrites(names map[string]bool, p *Provisioning) ([]jobs.ParameterWrite, error) {
	w := &writer{names: names}
	w.set(ManagementServer+"ConnReqXMPPConnection", ConnectionReference(p.ConnectionPath), "xsd:string", true)
	w.set(ManagementServer+"ConnReqAllowedJabberIDs", p.ACSJID, "xsd:string", false)
	if name := vendorParam(names, ManagementServer, "XMPP_Enable"); name != "" {
		w.set(name, "true", "xsd:boolean", false)
	}
	return w.result()
}

// VerifyPaths is the VERIFY step's GetParameterValues. ConnReqJabberID is
// always read (it is the proof); the Connection's own status leaves only
// when discovery saw them, since a missing name would fault the whole read.
func VerifyPaths(names map[string]bool, p *Provisioning) []string {
	paths := []string{ManagementServer + "ConnReqJabberID"}
	for _, leaf := range []string{"Status", "JabberID", "TLSEstablished"} {
		if _, ok := names[p.ConnectionPath+leaf]; ok {
			paths = append(paths, p.ConnectionPath+leaf)
		}
	}
	return paths
}
