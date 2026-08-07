package cwmp

import (
	"sync"
	"time"
)

// ProbeStepName identifies one step in the Phase 0 capability probe
// sequence (build plan §4 Phase 0).
type ProbeStepName string

const (
	StepGetRPCMethods        ProbeStepName = "GetRPCMethods"
	StepGetParameterNamesD2  ProbeStepName = "GetParameterNames(Device.)"
	StepGetParameterNamesIGD ProbeStepName = "GetParameterNames(InternetGatewayDevice.)"
)

// ProbeResults accumulates what a device's probe sequence discovered:
// supported RPC methods and which data model root(s) it answers under.
// This is the raw material for the device compatibility matrix (v3 §14
// Phase 0 deliverable).
type ProbeResults struct {
	RPCMethods        []string
	Device2Supported  bool
	Device2ParamCount int
	IGD1Supported     bool
	IGD1ParamCount    int
	Faults            map[ProbeStepName]string
}

// ProbeSession tracks one device's progress through the probe sequence.
// CWMP RPCs inside a session are strictly serial (v3 §2.3 / §5.4): the ACS
// sends one RPC and must wait for that response before sending the next.
// This type enforces that by only ever having one "current" step in
// flight, mirroring the corrected v3 session state machine at Phase 0
// scale (full session timers/state names land in later phases).
type ProbeSession struct {
	mu             sync.Mutex
	DeviceKey      string
	DeviceID       DeviceID
	EventCodes     []string
	Results        ProbeResults
	queue          []ProbeStepName
	current        ProbeStepName
	currentRequest []byte
	CreatedAt      time.Time
	LastActivity   time.Time
	Done           bool
}

func newProbeSession(deviceKey string, deviceID DeviceID, events []string) *ProbeSession {
	now := time.Now()
	return &ProbeSession{
		DeviceKey:  deviceKey,
		DeviceID:   deviceID,
		EventCodes: events,
		Results:    ProbeResults{Faults: map[ProbeStepName]string{}},
		queue: []ProbeStepName{
			StepGetRPCMethods,
			StepGetParameterNamesD2,
			StepGetParameterNamesIGD,
		},
		CreatedAt:    now,
		LastActivity: now,
	}
}

// NextRequest pops the next queued probe step and renders its RPC
// request. ok=false means the probe sequence is complete and the session
// should be closed (empty HTTP response).
//
// Enforces one in-flight RPC per session (design doc v3 §5.4 / §19.1 —
// the corrected model from v2, which had fired queued RPCs without
// waiting for responses): if a step is already in flight, this returns
// the same cached request instead of advancing the queue, so a duplicate
// or retried CPE POST is idempotent rather than skipping ahead.
func (s *ProbeSession) NextRequest(id string) (body []byte, step ProbeStepName, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current != "" {
		return s.currentRequest, s.current, true
	}

	if len(s.queue) == 0 {
		s.Done = true
		return nil, "", false
	}

	step = s.queue[0]
	s.queue = s.queue[1:]
	s.LastActivity = time.Now()

	switch step {
	case StepGetRPCMethods:
		body = RenderGetRPCMethods(id)
	case StepGetParameterNamesD2:
		body = RenderGetParameterNames(id, "Device.", false)
	case StepGetParameterNamesIGD:
		body = RenderGetParameterNames(id, "InternetGatewayDevice.", false)
	default:
		return nil, "", false
	}

	s.current = step
	s.currentRequest = body
	return body, step, true
}

// CompleteCurrent records the CPE's response (or fault) to whichever probe
// step was last dispatched, then clears the in-flight marker so the next
// NextRequest call can proceed.
func (s *ProbeSession) CompleteCurrent(body InboundBody) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastActivity = time.Now()

	step := s.current
	s.current = ""
	s.currentRequest = nil

	if body.Fault != nil {
		s.Results.Faults[step] = body.Fault.CWMPCode() + " " + body.Fault.CWMPMessage()
		return
	}

	switch step {
	case StepGetRPCMethods:
		if body.GetRPCMethodsResponse != nil {
			s.Results.RPCMethods = body.GetRPCMethodsResponse.MethodList
		}
	case StepGetParameterNamesD2:
		if body.GetParameterNamesResponse != nil {
			s.Results.Device2Supported = true
			s.Results.Device2ParamCount = len(body.GetParameterNamesResponse.ParameterList)
		}
	case StepGetParameterNamesIGD:
		if body.GetParameterNamesResponse != nil {
			s.Results.IGD1Supported = true
			s.Results.IGD1ParamCount = len(body.GetParameterNamesResponse.ParameterList)
		}
	}
}

// Snapshot returns a locked, deep-copied view of session identity and
// results — safe for a caller (e.g. the HTTP handler writing the
// compatibility-matrix record) to read after the probe sequence has
// finished, without racing any in-flight mutation.
func (s *ProbeSession) Snapshot() (deviceID DeviceID, eventCodes []string, results ProbeResults) {
	s.mu.Lock()
	defer s.mu.Unlock()

	faults := make(map[ProbeStepName]string, len(s.Results.Faults))
	for k, v := range s.Results.Faults {
		faults[k] = v
	}
	results = s.Results
	results.Faults = faults
	results.RPCMethods = append([]string(nil), s.Results.RPCMethods...)
	eventCodes = append([]string(nil), s.EventCodes...)
	return s.DeviceID, eventCodes, results
}

// SessionStore holds in-memory probe sessions keyed by device natural key
// (OUI+SerialNumber). Phase 0 explicitly needs no database (v3 §14 Phase
// 0: "no database needed initially" / build plan §4 Phase 0), so this is
// process-local and lost on restart — acceptable for a lab harness;
// durable device/session state is Phase 1's job (build plan §4 Phase 1).
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*ProbeSession
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]*ProbeSession{}}
}

// StartOrResume returns the in-flight probe session for a device, creating
// a fresh one if this is a new device or the previous probe sequence
// already finished (a later re-Inform, e.g. periodic, starts a new probe
// rather than reusing stale results).
func (s *SessionStore) StartOrResume(deviceID DeviceID, events []string) *ProbeSession {
	key := deviceID.NaturalKey()

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.sessions[key]; ok && !existing.Done {
		existing.mu.Lock()
		existing.EventCodes = events
		existing.mu.Unlock()
		return existing
	}

	session := newProbeSession(key, deviceID, events)
	s.sessions[key] = session
	return session
}

// Get returns the session for a device natural key, if one exists.
func (s *SessionStore) Get(deviceKey string) (*ProbeSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[deviceKey]
	return session, ok
}

// All returns a snapshot of every known session, for building the
// compatibility matrix.
func (s *SessionStore) All() []*ProbeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*ProbeSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

// Sweep removes sessions idle longer than maxIdle, so an ACS left running
// against a lab CPE that stopped mid-probe doesn't leak memory.
func (s *SessionStore) Sweep(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sess := range s.sessions {
		sess.mu.Lock()
		stale := sess.LastActivity.Before(cutoff)
		sess.mu.Unlock()
		if stale {
			delete(s.sessions, key)
		}
	}
}
