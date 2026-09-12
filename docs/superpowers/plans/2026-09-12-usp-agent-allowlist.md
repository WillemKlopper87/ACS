# USP Agent Allowlist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close `cmd/uspc`'s last documented security gap — accept USP agent connections only from allowed networks (a CIDR allowlist) and only for identities already known to ACS (a `devices` row must already exist), refusing and closing the connection otherwise.

**Architecture:** Two independent gates. Network-level: a filtering `net.Listener` in front of the WebSocket transport and a mochi-mqtt `OnConnectAuthenticate` hook in front of the MQTT transport, both consulting one `ACS_USP_ALLOWED_CIDRS` config value, empty = permissive. Identity-level: `devices.Repository.UpsertFromOnBoard` is renamed to `ReconcileFromOnBoard` and its contract changes from insert-or-update to update-only — an unknown OUI+SerialNumber returns a new `ErrUnknownDevice` instead of creating a row, and `cmd/uspc` closes the connection on that error.

**Tech Stack:** Go, `github.com/mochi-mqtt/server/v2` (embedded MQTT broker, already a dependency), PostgreSQL, `internal/netguard` (reused only for its CIDR-string parser).

**Spec:** `docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md`

## Global Constraints

- Empty `ACS_USP_ALLOWED_CIDRS` is permissive (matches `internal/netguard`'s existing default-permissive-until-configured convention) — this is NOT the same check as `netguard.Policy.CheckIP`: that method also always forbids loopback/link-local/multicast/unspecified addresses regardless of policy, a rule that exists for outbound SSRF protection and does not apply to this inbound gate (loopback is a normal, expected agent source in local dev/CI/same-host deployments). Build a separate, smaller check — do not call `netguard.Policy.CheckIP` for this gate. Reusing `netguard.ParseCIDRList` (pure string parsing, no policy semantics) is fine and required for consistency.
- The identity gate is unconditional — no config flag disables it. "Known" means a `devices` row already exists for that OUI+SerialNumber, via `PreRegister`, a prior CWMP Inform, or a prior USP onboarding.
- CWMP's own `UpsertFromInform` is untouched by this plan; it keeps its existing zero-touch auto-provisioning behavior.
- An unknown-identity refusal closes the connection immediately (`mtp.Conn.Close(reason)`), not a silent inert log.
- No new operator-facing UI/API surface — `PreRegister`'s existing bulk-import endpoint is reused as-is.
- Naming: `ACS_USP_ALLOWED_CIDRS` (mirrors the existing `ACS_DEVICE_NET_ALLOWED_CIDRS` naming convention for `internal/netguard`'s own CIDR var, per README.md).

---

### Task 1: `internal/usp/mtp` — network-level CIDR gate

**Files:**
- Create: `backend/internal/usp/mtp/allowlist.go`
- Create: `backend/internal/usp/mtp/allowlist_test.go`
- Modify: `backend/internal/usp/mtp/websocket.go` (add `AllowedCIDRs` to `WebSocketConfig`, wrap the listener in `Start`)
- Modify: `backend/internal/usp/mtp/mqtt.go` (add `AllowedCIDRs` to `MQTTConfig`, register the new hook in `NewMQTT`)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `mtp.WebSocketConfig.AllowedCIDRs []*net.IPNet`, `mtp.MQTTConfig.AllowedCIDRs []*net.IPNet` — Task 2 sets both from one parsed config value.

- [ ] **Step 1: Write `allowlist.go`**

```go
// Package mtp's allowlist.go: the network-level half of the USP agent
// allowlist (design docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md
// S2.1). A remote IP outside the configured CIDRs never completes a
// protocol handshake on either transport.
package mtp

import (
	"log/slog"
	"net"
)

// ipAllowed reports whether ip is permitted by cidrs: true when cidrs is
// empty (permissive, matching internal/netguard's own
// default-permissive-until-configured convention for CIDR allowlists
// elsewhere in this codebase), or when ip falls inside at least one of
// them.
//
// Deliberately not internal/netguard.Policy.CheckIP: that method also
// always forbids loopback/link-local/multicast/unspecified addresses
// regardless of policy -- a rule that exists to stop operator-influenced
// OUTBOUND connections from reaching internal targets (SSRF). Loopback is
// a normal, expected source for an inbound USP agent connection in local
// development, CI, and a same-host deployment, so this check has no such
// always-forbidden classes.
func ipAllowed(cidrs []*net.IPNet, ip net.IP) bool {
	if len(cidrs) == 0 {
		return true
	}
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// filteringListener wraps a net.Listener so Accept only ever returns a
// connection whose remote IP is allowed by cidrs. A disallowed connection
// is accepted (so it can be closed cleanly rather than left to the OS to
// eventually reset) and then immediately closed without being returned --
// the peer sees a TCP connection that opens and closes with no data
// exchanged, and Accept loops to the next pending connection rather than
// surfacing an error for what is, from the caller's perspective, routine
// traffic to expect and ignore.
type filteringListener struct {
	net.Listener
	cidrs []*net.IPNet
	log   *slog.Logger
}

func (fl *filteringListener) Accept() (net.Conn, error) {
	for {
		conn, err := fl.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
		var ip net.IP
		if splitErr == nil {
			ip = net.ParseIP(host)
		}
		if ip == nil || !ipAllowed(fl.cidrs, ip) {
			fl.log.Warn("mtp: rejecting connection from a disallowed network", "remote_addr", conn.RemoteAddr().String())
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

// wrapWithAllowlist returns ln unchanged when cidrs is empty (no
// wrapping overhead for the common permissive case), or a
// *filteringListener otherwise.
func wrapWithAllowlist(ln net.Listener, cidrs []*net.IPNet, log *slog.Logger) net.Listener {
	if len(cidrs) == 0 {
		return ln
	}
	return &filteringListener{Listener: ln, cidrs: cidrs, log: log}
}
```

- [ ] **Step 2: Write `allowlist_test.go`**

```go
package mtp

import (
	"log/slog"
	"net"
	"testing"
)

func mustParseCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", s, err)
	}
	return n
}

func TestIPAllowedEmptyIsPermissive(t *testing.T) {
	if !ipAllowed(nil, net.ParseIP("203.0.113.5")) {
		t.Error("ipAllowed with no CIDRs = false, want true (permissive default)")
	}
}

func TestIPAllowedContainment(t *testing.T) {
	cidrs := []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}
	if !ipAllowed(cidrs, net.ParseIP("10.1.2.3")) {
		t.Error("ipAllowed(10.1.2.3) inside 10.0.0.0/8 = false, want true")
	}
	if ipAllowed(cidrs, net.ParseIP("203.0.113.5")) {
		t.Error("ipAllowed(203.0.113.5) outside 10.0.0.0/8 = true, want false")
	}
}

func TestIPAllowedLoopbackNotSpeciallyForbidden(t *testing.T) {
	// Unlike netguard.Policy.CheckIP, loopback is ordinary here: allowed
	// when it falls in an explicit CIDR, not unconditionally forbidden.
	cidrs := []*net.IPNet{mustParseCIDR(t, "127.0.0.0/8")}
	if !ipAllowed(cidrs, net.ParseIP("127.0.0.1")) {
		t.Error("ipAllowed(127.0.0.1) inside an explicit 127.0.0.0/8 allowlist = false, want true")
	}
}

// TestFilteringListenerRejectsDisallowedRemote proves the WebSocket
// listener wrapper actually rejects at the TCP-accept level: a CIDR that
// deliberately excludes loopback closes a real local dial before any WS
// upgrade response.
func TestFilteringListenerRejectsDisallowedRemote(t *testing.T) {
	ws, err := NewWebSocket(WebSocketConfig{
		Addr:           "127.0.0.1:0",
		AllowPlaintext: true,
		AllowedCIDRs:   []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, // excludes 127.0.0.1
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	_, url := startedWS(t, ws)
	if _, _, err := dialRaw(url); err == nil {
		t.Error("dial from disallowed 127.0.0.1 succeeded, want a connection error")
	}
}

// TestFilteringListenerAllowsPermittedRemote proves the positive case:
// a CIDR that includes loopback lets a real local dial complete its WS
// upgrade normally.
func TestFilteringListenerAllowsPermittedRemote(t *testing.T) {
	ws, err := NewWebSocket(WebSocketConfig{
		Addr:           "127.0.0.1:0",
		AllowPlaintext: true,
		AllowedCIDRs:   []*net.IPNet{mustParseCIDR(t, "127.0.0.0/8")},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := newRecordingHandler()
	_, url := startedWSWithHandler(t, ws, h)
	c := dial(t, url, "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect after an allowed dial")
}
```

`startedWS`/`startedWSWithHandler`/`dialRaw` do not exist yet in `websocket_test.go` — `startWS` there always builds its own `WebSocketConfig` internally and has no way to accept a pre-built `*WebSocket` or return connect errors instead of failing the test. Rather than changing `startWS`'s signature (used by many existing tests), add these two small helpers to `websocket_test.go` in this same step:

```go
// startedWS starts ws (already constructed by the caller, e.g. with a
// non-default config like AllowedCIDRs) and returns it with its ws://
// URL, exactly like startWS but for a caller that needs to control
// construction. h is a minimal handler; use startedWSWithHandler when the
// test needs to observe connects.
func startedWS(t *testing.T, ws *WebSocket) (*WebSocket, string) {
	t.Helper()
	return startedWSWithHandler(t, ws, newRecordingHandler())
}

func startedWSWithHandler(t *testing.T, ws *WebSocket, h Handler) (*WebSocket, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = ws.Stop(context.Background()) })
	if err := ws.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	return ws, "ws://" + ws.Addr() + "/usp"
}

// dialRaw attempts a WebSocket dial and returns the error instead of
// failing the test -- for a test that expects the dial to fail (a
// disallowed remote address).
func dialRaw(url string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"v1.usp"}})
}
```

- [ ] **Step 3: Run the new tests to verify they fail (compile errors — `AllowedCIDRs` doesn't exist yet)**

Run: `cd backend && go test ./internal/usp/mtp/... -run TestIPAllowed -v`
Expected: FAIL to compile — `WebSocketConfig` has no field `AllowedCIDRs`.

- [ ] **Step 4: Add `AllowedCIDRs` to `WebSocketConfig` and wrap the listener**

In `websocket.go`, add to `WebSocketConfig`:

```go
	// AllowedCIDRs, when non-empty, restricts accepted connections to
	// remote addresses inside one of these networks -- empty is
	// permissive (design S2.1). Checked at the raw TCP accept, before
	// any TLS or WebSocket handshake.
	AllowedCIDRs []*net.IPNet
```

In `Start`, right after the existing `ln, err := net.Listen("tcp", w.cfg.Addr)` block and before the `if w.cfg.TLS != nil` block, insert:

```go
	ln = wrapWithAllowlist(ln, w.cfg.AllowedCIDRs, w.log)
```

Add `"net"` to `websocket.go`'s imports if not already present (it is — `net.Listen` is already called there).

- [ ] **Step 5: Add `AllowedCIDRs` to `MQTTConfig` and register the allowlist hook**

In `mqtt.go`, add to `MQTTConfig`:

```go
	// AllowedCIDRs, when non-empty, restricts accepted CONNECTs to remote
	// addresses inside one of these networks -- empty is permissive
	// (design S2.1). Enforced by allowlistHook.OnConnectAuthenticate,
	// before any CONNACK is sent.
	AllowedCIDRs []*net.IPNet
```

Add this hook type (near `mqttDisconnectHook`, same file):

```go
// allowlistHook rejects a CONNECT from a remote address outside cidrs,
// the network-level half of the USP agent allowlist (design S2.1). It is
// registered alongside auth.AllowHook, not instead of it: AllowHook still
// grants topic pub/sub access (unrelated, out of this plan's scope, S7 of
// the design doc); this hook only gates whether a CONNECT is accepted at
// all. OnConnectAuthenticate returning false makes mochi-mqtt refuse the
// CONNECT and close the connection before any CONNACK is sent -- no USP
// record is ever exchanged with a rejected client.
type allowlistHook struct {
	mqttserver.HookBase
	cidrs []*net.IPNet
	log   *slog.Logger
}

func (h *allowlistHook) ID() string { return "usp-allowlist" }

func (h *allowlistHook) Provides(b byte) bool {
	return b == mqttserver.OnConnectAuthenticate
}

func (h *allowlistHook) OnConnectAuthenticate(cl *mqttserver.Client, _ packets.Packet) bool {
	host, _, err := net.SplitHostPort(cl.Net.Remote)
	if err != nil {
		h.log.Warn("mtp: rejecting MQTT client with unparseable remote address", "remote", cl.Net.Remote, "error", err)
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ipAllowed(h.cidrs, ip) {
		h.log.Warn("mtp: rejecting MQTT client from a disallowed network", "remote", cl.Net.Remote)
		return false
	}
	return true
}
```

In `NewMQTT`, right after the existing `if err := server.AddHook(&mqttDisconnectHook{m: m}, nil); ...` block, insert:

```go
	if len(cfg.AllowedCIDRs) > 0 {
		if err := server.AddHook(&allowlistHook{cidrs: cfg.AllowedCIDRs, log: log}, nil); err != nil {
			return nil, fmt.Errorf("mtp: MQTT add allowlist hook: %w", err)
		}
	}
```

Add `"net"` to `mqtt.go`'s imports.

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `cd backend && go test ./internal/usp/mtp/... -run 'TestIPAllowed|TestFilteringListener' -v`
Expected: PASS, all 5 new tests.

- [ ] **Step 7: Write a unit test for the MQTT hook**

Add to `mqtt_test.go`:

```go
func TestAllowlistHookRejectsDisallowedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
		AllowedCIDRs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	hook := &allowlistHook{cidrs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, log: slog.Default()}
	cl := newMQTTAgentClient(t, m, "outside-allowlist", 5)
	cl.Net.Remote = "203.0.113.5:12345" // outside 10.0.0.0/8
	if hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate for a disallowed remote = true, want false")
	}
}

func TestAllowlistHookAllowsPermittedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	hook := &allowlistHook{cidrs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, log: slog.Default()}
	cl := newMQTTAgentClient(t, m, "inside-allowlist", 5)
	cl.Net.Remote = "10.1.2.3:12345"
	if !hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate for an allowed remote = false, want true")
	}
}

func TestAllowlistHookPermissiveWhenEmpty(t *testing.T) {
	hook := &allowlistHook{log: slog.Default()} // no cidrs
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	cl := newMQTTAgentClient(t, m, "any-remote", 5)
	cl.Net.Remote = "203.0.113.5:12345"
	if !hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate with an empty allowlist = false, want true (permissive)")
	}
}
```

- [ ] **Step 8: Run the full package test suite**

Run: `cd backend && go test ./internal/usp/mtp/... -v`
Expected: PASS, no regressions in any existing WebSocket/MQTT test.

- [ ] **Step 9: `gofmt` and `vet`**

Run: `cd backend && gofmt -l internal/usp/mtp/ && go vet ./internal/usp/mtp/...`
Expected: no output from `gofmt -l` (already formatted), `vet` clean.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/usp/mtp/allowlist.go backend/internal/usp/mtp/allowlist_test.go backend/internal/usp/mtp/websocket.go backend/internal/usp/mtp/websocket_test.go backend/internal/usp/mtp/mqtt.go backend/internal/usp/mtp/mqtt_test.go
git commit -m "$(cat <<'EOF'
feat(mtp): network-level CIDR allowlist for both USP transports

A filtering net.Listener gates WebSocket at the raw TCP accept; a
mochi-mqtt OnConnectAuthenticate hook gates MQTT before any CONNACK.
Both consult the same permissive-when-empty CIDR policy, deliberately
not netguard.Policy.CheckIP -- that method also always forbids
loopback, wrong for an inbound agent gate where loopback is a normal
source in local dev/CI.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `cmd/uspc` — wire `ACS_USP_ALLOWED_CIDRS` into both transports

**Files:**
- Modify: `backend/cmd/uspc/config.go`
- Modify: `backend/cmd/uspc/config_test.go`
- Modify: `backend/cmd/uspc/main.go`

**Interfaces:**
- Consumes: `mtp.WebSocketConfig.AllowedCIDRs`, `mtp.MQTTConfig.AllowedCIDRs` (Task 1).
- Produces: `serviceConfig.AllowedCIDRs []*net.IPNet`, consumed by Task 4 for nothing further (Task 4 only touches identity, not this field) — this task is self-contained end to end.

- [ ] **Step 1: Read the existing config test file's pattern**

Read `backend/cmd/uspc/config_test.go` in full before writing the new test below — match its exact helper functions (likely a `validConfigEnv()`-style map plus a `getenvFrom(map[string]string)` closure) rather than inventing a new one.

- [ ] **Step 2: Write the failing test**

Add to `config_test.go` (adjust the exact helper names to match what Step 1 found — the shape below assumes a `validConfigEnv()` map-returning helper and a `getenvFrom` closure; if the file's real helpers differ, use those instead, keeping the same assertions):

```go
func TestLoadConfigParsesAllowedCIDRs(t *testing.T) {
	env := validConfigEnv()
	env["ACS_USP_ALLOWED_CIDRS"] = "10.0.0.0/8,192.168.1.0/24"
	cfg, err := loadConfig(getenvFrom(env), slog.Default())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.AllowedCIDRs) != 2 {
		t.Fatalf("AllowedCIDRs = %v, want 2 entries", cfg.AllowedCIDRs)
	}
}

func TestLoadConfigAllowedCIDRsEmptyIsPermissive(t *testing.T) {
	env := validConfigEnv() // no ACS_USP_ALLOWED_CIDRS set
	cfg, err := loadConfig(getenvFrom(env), slog.Default())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.AllowedCIDRs) != 0 {
		t.Errorf("AllowedCIDRs = %v, want empty when unset", cfg.AllowedCIDRs)
	}
}

func TestLoadConfigRejectsInvalidCIDR(t *testing.T) {
	env := validConfigEnv()
	env["ACS_USP_ALLOWED_CIDRS"] = "not-a-cidr"
	if _, err := loadConfig(getenvFrom(env), slog.Default()); err == nil {
		t.Error("loadConfig with an invalid CIDR succeeded, want an error")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./cmd/uspc/... -run TestLoadConfigAllowedCIDRs -v`
Expected: FAIL to compile — no `AllowedCIDRs` field on `serviceConfig`.

- [ ] **Step 4: Implement**

In `config.go`, add to the imports: `"net"` and `"acs/internal/netguard"`.

Add to `serviceConfig`:

```go
	// AllowedCIDRs is the network-level half of the USP agent allowlist
	// (design docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md
	// S2.1) -- empty is permissive, matching netguard's own
	// default-permissive-until-configured convention.
	AllowedCIDRs []*net.IPNet
```

In `loadConfig`, after the `postgresDSN` block and before the `if len(problems) > 0` check, add:

```go
	allowedCIDRs, err := netguard.ParseCIDRList(getenv("ACS_USP_ALLOWED_CIDRS"))
	if err != nil {
		problems = append(problems, fmt.Sprintf("ACS_USP_ALLOWED_CIDRS: %v", err))
	}
```

In the `return serviceConfig{...}` literal, add:

```go
		AllowedCIDRs: allowedCIDRs,
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./cmd/uspc/... -run TestLoadConfigAllowedCIDRs -v`
Expected: PASS, all 3 new tests.

- [ ] **Step 6: Wire into both transports in `main.go`**

In `newTransports`, add `AllowedCIDRs: cfg.AllowedCIDRs` to both the `mtp.WebSocketConfig{...}` and `mtp.MQTTConfig{...}` literals.

- [ ] **Step 7: Run the full `cmd/uspc` test suite**

Run: `cd backend && go test ./cmd/uspc/... -v 2>&1 | tail -40`
Expected: PASS, no regressions (real Postgres available locally, `ACS_TEST_POSTGRES_DSN` set the same way every prior task in this session has used it).

- [ ] **Step 8: `gofmt` and `vet`**

Run: `cd backend && gofmt -l cmd/uspc/ && go vet ./cmd/uspc/...`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add backend/cmd/uspc/config.go backend/cmd/uspc/config_test.go backend/cmd/uspc/main.go
git commit -m "$(cat <<'EOF'
feat(uspc): wire ACS_USP_ALLOWED_CIDRS into both USP transports

One config value, parsed once, applied identically to the WebSocket
listener wrapper and the MQTT allowlist hook (both from the prior
commit). main.go's stale "no allowlist" warning is left alone until
the identity-level gate lands too (next task) -- it is still
literally true until then.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `internal/devices` — `ReconcileFromOnBoard` (reject-unless-known)

**Files:**
- Modify: `backend/internal/devices/usp.go`
- Modify: `backend/internal/devices/usp_test.go`

**Interfaces:**
- Consumes: `devices.Repository.GetByOUIserial` (existing, for reference only — not called directly; the new SQL does the lookup-and-update in one statement).
- Produces: `Repository.ReconcileFromOnBoard(ctx, oui, productClass, serialNumber string) (*Device, error)` (renamed from `UpsertFromOnBoard`, new reject-unless-known contract), `ErrUnknownDevice` sentinel error — Task 4 consumes both.

- [ ] **Step 1: Read the current file in full**

Read `backend/internal/devices/usp.go` (already quoted in the design's research, reproduced here for the exact current SQL — confirm it still matches before editing):

```go
func (r *Repository) UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error) {
	if oui == "" || serialNumber == "" {
		return nil, ErrEmptyIdentity
	}
	id := cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      data_model_root, online_status, first_seen_at, last_updated_at, management_protocols)
		VALUES ($1, $2, '', $3, $4, $5, 'DEVICE2', 'ONLINE', now(), now(), ARRAY['USP'])
		ON CONFLICT (oui_serial) DO UPDATE SET
			online_status = 'ONLINE',
			last_updated_at = now(),
			management_protocols = CASE WHEN 'USP' = ANY(devices.management_protocols) THEN devices.management_protocols ELSE array_append(devices.management_protocols, 'USP') END
		RETURNING `+deviceColumns, uuid.New().String(), id.NaturalKey(), id.OUI, id.ProductClass, id.SerialNumber)

	return scanDevice(row)
}
```

- [ ] **Step 2: Write the failing tests**

Rewrite `usp_test.go` in full with this content (every existing test updated for the new reject-unless-known contract, plus two new ones):

```go
package devices

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"acs/internal/cwmp"
)

// TestReconcileFromOnBoardUpdatesKnownDevice replaces
// TestUpsertFromOnBoardCreates: under the new contract, ReconcileFromOnBoard
// never creates a device -- it only updates one that already exists (here,
// via PreRegister, standing in for an operator's bulk import ahead of the
// device's first contact).
func TestReconcileFromOnBoardUpdatesKnownDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	pre, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil)
	if err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if d.ID != pre.ID {
		t.Fatalf("ReconcileFromOnBoard returned device %s, want the pre-registered %s", d.ID, pre.ID)
	}
	protocols := managementProtocols(t, ctx, r, d.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols = %v, want [USP]", protocols)
	}
}

// TestReconcileFromOnBoardRejectsUnknownDevice covers this plan's central
// gate: an OUI+SerialNumber with no existing devices row is refused, not
// auto-created.
func TestReconcileFromOnBoardRejectsUnknownDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	_, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("ReconcileFromOnBoard for an unknown device = %v, want ErrUnknownDevice", err)
	}

	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM devices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("devices row count = %d after a rejected onboard, want 0 (nothing created)", count)
	}
}

// TestReconcileFromOnBoardMatchesExistingCWMPDevice is the checklist's
// central dual-stack assertion: a device onboarded first via CWMP
// (UpsertFromInform) and then reconciled via USP (ReconcileFromOnBoard)
// lands on the SAME devices row (same natural oui_serial key), and
// management_protocols ends up containing both 'CWMP' and 'USP' — neither
// overwrites the other.
func TestReconcileFromOnBoardMatchesExistingCWMPDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}

	uspDevice, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}

	if uspDevice.ID != cwmpDevice.ID {
		t.Fatalf("ReconcileFromOnBoard matched a different row: %s != %s", uspDevice.ID, cwmpDevice.ID)
	}
	protocols := managementProtocols(t, ctx, r, cwmpDevice.ID)
	if !containsAll(protocols, "CWMP", "USP") {
		t.Errorf("management_protocols = %v, want to contain both CWMP and USP", protocols)
	}
}

// TestReconcileFromOnBoardIdempotent: calling ReconcileFromOnBoard twice
// for the same known device must not produce duplicate 'USP' entries in
// management_protocols.
func TestReconcileFromOnBoardIdempotent(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil); err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d1, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("first ReconcileFromOnBoard: %v", err)
	}
	d2, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("second ReconcileFromOnBoard: %v", err)
	}
	if d1.ID != d2.ID {
		t.Fatalf("second ReconcileFromOnBoard matched a different row: %s != %s", d2.ID, d1.ID)
	}
	protocols := managementProtocols(t, ctx, r, d1.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols after repeated ReconcileFromOnBoard = %v, want exactly [USP] (no duplicates)", protocols)
	}
}

// TestReconcileFromOnBoardSetsDataModelRootDevice2WhenUnknown covers the
// design's data_model_root fix: a device pre-registered via PreRegister
// (which never sets data_model_root, leaving it at the column's own
// 'UNKNOWN' default) and then onboarded via USP for the first time must
// end up DEVICE2, not stuck at UNKNOWN forever.
func TestReconcileFromOnBoardSetsDataModelRootDevice2WhenUnknown(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	pre, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil)
	if err != nil {
		t.Fatalf("PreRegister: %v", err)
	}
	if pre.DataModelRoot != "UNKNOWN" {
		t.Fatalf("pre-registered device DataModelRoot = %q, want UNKNOWN (test assumption wrong)", pre.DataModelRoot)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if d.DataModelRoot != DataModelRootDevice2 {
		t.Errorf("DataModelRoot = %q, want %q", d.DataModelRoot, DataModelRootDevice2)
	}
}

// TestReconcileFromOnBoardDoesNotOverwriteExistingDataModelRoot covers the
// other half: a CWMP-discovered data_model_root (which might legitimately
// be IGD1) must survive a later USP onboarding of the same physical
// device.
func TestReconcileFromOnBoardDoesNotOverwriteExistingDataModelRoot(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}
	if err := r.UpdateDataModelRoot(ctx, cwmpDevice.ID, DataModelRootIGD1); err != nil {
		t.Fatalf("UpdateDataModelRoot: %v", err)
	}

	uspDevice, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if uspDevice.DataModelRoot != DataModelRootIGD1 {
		t.Errorf("DataModelRoot after USP onboarding of an existing CWMP(IGD1) device = %q, want unchanged %q", uspDevice.DataModelRoot, DataModelRootIGD1)
	}
}

// TestReconcileFromOnBoardRejectsEmptyOUI and
// TestReconcileFromOnBoardRejectsEmptySerialNumber cover final-review
// finding 1 (from the USP job dispatch plan): an empty OUI or
// SerialNumber must be rejected before any query runs, mirroring
// cmd/acs/session.go's CWMP Inform guard. These deliberately construct a
// bare *Repository{} rather than going through newDevicesTestRepo/a live
// Postgres connection: the guard must fire before any query is issued, so
// a nil *sql.DB proves that (a query attempt against a nil db would
// panic, failing the test) without needing ACS_TEST_POSTGRES_DSN.
func TestReconcileFromOnBoardRejectsEmptyOUI(t *testing.T) {
	r := &Repository{}
	_, err := r.ReconcileFromOnBoard(context.Background(), "", "Router", "ABC123")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("ReconcileFromOnBoard with empty OUI = %v, want ErrEmptyIdentity", err)
	}
}

func TestReconcileFromOnBoardRejectsEmptySerialNumber(t *testing.T) {
	r := &Repository{}
	_, err := r.ReconcileFromOnBoard(context.Background(), "001122", "Router", "")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("ReconcileFromOnBoard with empty SerialNumber = %v, want ErrEmptyIdentity", err)
	}
}

// TestReconcileFromOnBoardAllowsEmptyProductClass proves the guard
// matches the CWMP Inform path's exact shape (session.go:218):
// ProductClass alone being empty is not rejected, only an empty OUI or
// SerialNumber is -- and that a device pre-registered with an empty
// ProductClass is still matched correctly.
func TestReconcileFromOnBoardAllowsEmptyProductClass(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "", "ABC123", nil, nil); err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard with empty ProductClass: %v", err)
	}
	if d.OUI != "001122" || d.SerialNumber != "ABC123" {
		t.Errorf("device = %+v, want OUI=001122 SerialNumber=ABC123", d)
	}
}

var _ = sql.ErrNoRows // referenced by usp.go's ReconcileFromOnBoard, not this file directly -- keeps goimports from flagging an unused import if this file is later trimmed
```

Remove the last line (`var _ = sql.ErrNoRows ...` and the now-unneeded `"database/sql"` import) if `go vet`/`goimports` doesn't actually flag it — it's a defensive placeholder only for the case where no other test in this file needs `database/sql` directly. Check by running `gofmt`/`go vet` in Step 6; delete both the line and the import if they turn out unnecessary.

- [ ] **Step 3: Run to verify the tests fail**

Run: `cd backend && go test ./internal/devices/... -run TestReconcileFromOnBoard -v`
Expected: FAIL to compile — no method `ReconcileFromOnBoard`, no `ErrUnknownDevice`.

- [ ] **Step 4: Rewrite `usp.go`**

Replace the whole file's `UpsertFromOnBoard` function and add the new sentinel error:

```go
package devices

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"acs/internal/cwmp"
)

// ErrEmptyIdentity is returned when a USP identity reconcile is attempted
// with an empty OUI or SerialNumber. This mirrors the CWMP Inform path's
// guard (cmd/acs/session.go's handleInform: "OUI and SerialNumber are
// required for a device identity") -- ProductClass may legitimately be
// empty, same as CWMP tolerates, but OUI and SerialNumber compose the
// natural key every device row is matched on. Without this guard, every
// empty-identity OnBoardRequest/GetResp collapses onto the same
// oui_serial = "+..." row, and usp_agents.LinkUspAgent's
// ON CONFLICT (device_id) DO UPDATE then silently steals that row's
// endpoint binding from whichever agent linked it last.
var ErrEmptyIdentity = errors.New("usp: OUI and SerialNumber are required for a device identity")

// ErrUnknownDevice is returned by ReconcileFromOnBoard when no devices
// row exists for the given identity -- the identity-level half of the USP
// agent allowlist (design
// docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md S2.2).
// A device becomes "known" via PreRegister (bulk import), a prior CWMP
// Inform, or a prior USP onboarding -- never via this function itself,
// which no longer creates rows.
var ErrUnknownDevice = errors.New("usp: no devices row exists for this identity -- pre-register the device before its first USP contact")

// ReconcileFromOnBoard records (or refreshes) a device from a USP
// OnBoardRequest Notify, landing it on the exact same oui_serial natural
// key (internal/cwmp.DeviceID.NaturalKey) a CWMP Inform for the same
// physical unit would produce — so a dual-stack device is one devices row,
// not two.
//
// Unlike its predecessor UpsertFromOnBoard, this never creates a devices
// row: an identity with no existing row returns ErrUnknownDevice. This is
// the identity-level gate of the USP agent allowlist -- a device becomes
// "known" via PreRegister, a prior CWMP Inform, or a prior USP onboarding
// (before this gate existed), never via this function.
//
// It additionally records 'USP' in management_protocols instead of
// 'CWMP', without disturbing whatever protocols that array already holds
// (a device onboarded first via CWMP keeps 'CWMP' and gains 'USP' here),
// and fills data_model_root in to DEVICE2 only when it is still at the
// devices table's own 'UNKNOWN' default (design spec §5.3: "data_model_root
// is always DEVICE2 for USP agents") -- a real CWMP-discovered root
// ('IGD1', or a CWMP-set 'DEVICE2') is never overwritten, only the
// genuinely-never-set case (e.g. a device that reached this row only via
// PreRegister, with no CWMP contact ever, then onboards via USP for the
// first time) is filled in.
func (r *Repository) ReconcileFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error) {
	if oui == "" || serialNumber == "" {
		return nil, ErrEmptyIdentity
	}
	id := cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}

	row := r.db.QueryRowContext(ctx, `
		UPDATE devices SET
			online_status = 'ONLINE',
			last_updated_at = now(),
			management_protocols = CASE WHEN 'USP' = ANY(management_protocols) THEN management_protocols ELSE array_append(management_protocols, 'USP') END,
			data_model_root = CASE WHEN data_model_root = 'UNKNOWN' THEN 'DEVICE2' ELSE data_model_root END
		WHERE oui_serial = $1
		RETURNING `+deviceColumns, id.NaturalKey())

	device, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownDevice
	}
	if err != nil {
		return nil, err
	}
	return device, nil
}

var _ = uuid.New // uuid is still used elsewhere in this package (PreRegister, UpsertFromInform); if goimports flags it as unused specifically in this file after this edit, remove the "github.com/google/uuid" import from usp.go's own import block -- it was only needed by the old INSERT's id column, which this UPDATE-only version no longer has.
```

The `var _ = uuid.New` line and its comment are a note for the implementer, not code to keep: run `goimports`/`go vet` after this edit and delete the `"github.com/google/uuid"` import from `usp.go` if it is indeed now unused in this file (the `UPDATE` no longer generates a new id), and delete that placeholder line entirely either way — it must not remain in the final file.

- [ ] **Step 5: Run to verify the tests pass**

Run: `cd backend && go test ./internal/devices/... -run TestReconcileFromOnBoard -v`
Expected: PASS, all 9 tests (2 new: rejects-unknown, updates-known; the rest renamed/adjusted).

- [ ] **Step 6: Run the full package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./internal/devices/... -v 2>&1 | tail -60 && gofmt -l internal/devices/ && go vet ./internal/devices/...`
Expected: all PASS, `gofmt -l` empty, `vet` clean. Remove the two placeholder lines noted in Steps 2 and 4 now if you haven't already — they must not survive into the commit.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/devices/usp.go backend/internal/devices/usp_test.go
git commit -m "$(cat <<'EOF'
feat(devices): ReconcileFromOnBoard rejects unknown USP identities

Renamed from UpsertFromOnBoard; the contract changes from
insert-or-update to update-only. An OUI+SerialNumber with no existing
devices row now returns ErrUnknownDevice instead of silently creating
one -- the identity-level half of the USP agent allowlist. A device
becomes known via PreRegister, a prior CWMP Inform, or a prior USP
onboarding.

Folds in the data_model_root fix this exposed: PreRegister never sets
it (defaults to 'UNKNOWN'), and the old ON CONFLICT UPDATE never
touched it either, so a pre-registered-then-USP-onboarded device
(no CWMP contact ever) was stuck at UNKNOWN forever. Now filled to
DEVICE2 only when still UNKNOWN, never overwriting a real
CWMP-discovered root.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `cmd/uspc` — wire the rename through, close connection on `ErrUnknownDevice`

**Files:**
- Modify: `backend/cmd/uspc/identity.go`
- Modify: `backend/cmd/uspc/identity_test.go`
- Modify: `backend/cmd/uspc/handler.go`
- Modify: `backend/cmd/uspc/handler_test.go`
- Modify: `backend/cmd/uspc/main_test.go`
- Modify: `backend/cmd/uspc/dispatcher_test.go` (comment wording only)
- Modify: `backend/cmd/uspc/main.go` (doc comment + startup log line)

**Interfaces:**
- Consumes: `devices.Repository.ReconcileFromOnBoard`, `devices.ErrUnknownDevice` (Task 3).
- Produces: nothing further — this is the last code task before CI/docs.

- [ ] **Step 1: Rename in `identity.go`**

In the `identityStore` interface, rename the first method:

```go
	ReconcileFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*devices.Device, error)
```

In `reconciler.onBoard`, change the call and its wrapped error text:

```go
	device, err := r.store.ReconcileFromOnBoard(ctx, ob.OUI, ob.ProductClass, ob.SerialNumber)
	if err != nil {
		return fmt.Errorf("reconcile onboard: %w", err)
	}
```

In `reconciler.fromProbeFallback`, the same:

```go
	device, err := r.store.ReconcileFromOnBoard(ctx, oui, productClass, serialNumber)
	if err != nil {
		return fmt.Errorf("reconcile probe fallback: %w", err)
	}
```

(The wrapped message drops the old `": upsert device"` segment — it no longer upserts.)

- [ ] **Step 2: Update `identity_test.go`'s fake**

Rename `fakeIdentityStore.UpsertFromOnBoard` to `ReconcileFromOnBoard` and change its body to reject-unless-known instead of auto-create. Replace the whole method:

```go
func (f *fakeIdentityStore) ReconcileFromOnBoard(_ context.Context, oui, productClass, serialNumber string) (*devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls = append(f.upsertCalls, onboardCall{oui, productClass, serialNumber})
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	// Mirrors devices.Repository.ReconcileFromOnBoard's real guard (final-
	// review finding 1): an empty OUI or SerialNumber is rejected before
	// any device row is looked up.
	if oui == "" || serialNumber == "" {
		return nil, devices.ErrEmptyIdentity
	}
	key := oui + "|" + productClass + "|" + serialNumber
	d, ok := f.devicesByKey[key]
	if !ok {
		return nil, devices.ErrUnknownDevice
	}
	return d, nil
}

// seedKnownDevice pre-populates f.devicesByKey as if the device had
// already been pre-registered or onboarded before -- the fake's
// equivalent of a real devices row existing ahead of a
// ReconcileFromOnBoard call, required under the reject-unless-known
// contract for any test that expects reconciliation to succeed.
func (f *fakeIdentityStore) seedKnownDevice(oui, productClass, serialNumber string) *devices.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextDeviceID++
	d := &devices.Device{
		ID:           fmt.Sprintf("device-%d", f.nextDeviceID),
		OUI:          oui,
		ProductClass: productClass,
		SerialNumber: serialNumber,
	}
	f.devicesByKey[oui+"|"+productClass+"|"+serialNumber] = d
	return d
}
```

Rename every call site in this file from `store.upsertCalls`/`r.onBoard`/`r.fromProbeFallback` unchanged (those aren't renamed), but every test that currently expects `onBoard`/`fromProbeFallback` to SUCCEED must call `store.seedKnownDevice(...)` first, since the fake no longer auto-creates. Specifically:

| Test | Change |
|---|---|
| `TestReconcilerOnBoard` | After `store := newFakeIdentityStore()`, add `store.seedKnownDevice("0025C2", "Gateway", "SN12345")` |
| `TestReconcilerFromProbeFallback` | Same: add `store.seedKnownDevice("0025C2", "Gateway", "SN12345")` after `store := newFakeIdentityStore()` |

`TestReconcilerOnBoardUpsertError`, `TestReconcilerOnBoardRejectsEmptyOUI`, `TestReconcilerOnBoardRejectsEmptySerialNumber`, `TestReconcilerFromProbeFallbackRejectsEmptyIdentity` are unaffected — each fails before or independent of the not-found check (the injected `upsertErr` and the empty-identity guard both short-circuit ahead of the lookup, matching the real function's order: injected/generic error first, then the empty-identity guard, then the lookup). `TestReconcilerDisconnect`/`TestReconcilerDisconnectSwallowsError` are unaffected (don't touch onboard at all).

Add two new tests at the end of the file:

```go
// TestReconcilerOnBoardRejectsUnknownDevice and
// TestReconcilerFromProbeFallbackRejectsUnknownDevice cover this plan's
// central gate at the reconciler layer: an identity with no seeded
// device is refused, and critically must never reach LinkUspAgent.
func TestReconcilerOnBoardRejectsUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore()
	r := newReconciler(store, slog.Default())
	c := &captureConn{id: agent}
	ob := &usp.OnBoardRequest{OUI: "0025C2", ProductClass: "Gateway", SerialNumber: "SN12345"}

	err := r.onBoard(context.Background(), c, ob)
	if !errors.Is(err, devices.ErrUnknownDevice) {
		t.Fatalf("onBoard() for an unseeded identity = %v, want devices.ErrUnknownDevice", err)
	}
	if len(store.linkCalls) != 0 {
		t.Errorf("linkCalls = %+v, want none: LinkUspAgent must not be called for an unknown device", store.linkCalls)
	}
}

func TestReconcilerFromProbeFallbackRejectsUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore()
	r := newReconciler(store, slog.Default())
	c := &captureConn{id: agent}

	err := r.fromProbeFallback(context.Background(), c, "0025C2", "Gateway", "SN12345")
	if !errors.Is(err, devices.ErrUnknownDevice) {
		t.Fatalf("fromProbeFallback() for an unseeded identity = %v, want devices.ErrUnknownDevice", err)
	}
	if len(store.linkCalls) != 0 {
		t.Errorf("linkCalls = %+v, want none: LinkUspAgent must not be called for an unknown device", store.linkCalls)
	}
}
```

- [ ] **Step 3: Run `identity_test.go` to verify it compiles and passes**

Run: `cd backend && go test ./cmd/uspc/... -run TestReconciler -v`
Expected: PASS, all tests including the 2 new ones.

- [ ] **Step 4: Update `handler_test.go`'s 8 affected tests**

The exact one-line insertion `store.seedKnownDevice("0025C2", "Gateway", "SN12345")`, placed immediately after `store := newFakeIdentityStore()`, is needed in exactly these 8 tests (identified by reading the whole file — every test that calls the `onBoardRequestMsg` helper, which exercises the real `ReconcileFromOnBoard` path rather than the `markReconciled` test-only backdoor):

| Test | Line (before this edit) |
|---|---|
| `TestHandlerOnBoardRequestReconciles` | after line 242 |
| `TestHandlerOnBoardRequestSendsResp` | after line 264 |
| `TestHandlerOnBoardRequestNoRespWhenNotRequested` | after line 294 |
| `TestHandlerProbeFallbackReconcilesOnce` | after line 307 |
| `TestHandlerOnBoardRequestSuppressesProbeFallback` | after line 338 |
| `TestHandlerDisconnectMarksUspAgent` | after line 362 |
| `TestHandlerDisconnectSkipsStaleConnectionAfterTakeover` | after line 405 |
| `TestHandlerDisconnectStaleEndpointAfterDifferentEndpointReconnect` | after line 459 |

(Line numbers are as of this plan's writing — locate each test by name, not by line number, since earlier edits in this same task may shift them; insert the seed call as the first statement after that test's `store := newFakeIdentityStore()` line.)

`TestHandlerDisconnectSkipsUnreconciledConnection` is unaffected (never calls `onBoardRequestMsg`).

- [ ] **Step 5: Run `handler_test.go`'s onboard/disconnect tests**

Run: `cd backend && go test ./cmd/uspc/... -run 'TestHandlerOnBoardRequest|TestHandlerProbeFallback|TestHandlerDisconnect' -v`
Expected: PASS, all 9 tests (8 fixed + the 1 unaffected one).

- [ ] **Step 6: Add the close-on-`ErrUnknownDevice` behavior in `handler.go`**

In `handleOnBoardRequest`, change:

```go
	if err != nil {
		h.logReconcileFailure(c, "failed to reconcile onboard request", err)
		return
	}
```

to:

```go
	if err != nil {
		h.logReconcileFailure(c, "failed to reconcile onboard request", err)
		if errors.Is(err, devices.ErrUnknownDevice) {
			_ = c.Close("agent identity not pre-registered")
		}
		return
	}
```

In `handleProbeFallback` (the second call site, around the existing `h.logReconcileFailure(c, "failed to reconcile via probe fallback", err)` line), make the identical change:

```go
	if err != nil {
		h.logReconcileFailure(c, "failed to reconcile via probe fallback", err)
		if errors.Is(err, devices.ErrUnknownDevice) {
			_ = c.Close("agent identity not pre-registered")
		}
		return
	}
```

Also extend `logReconcileFailure`'s own doc comment (it already documents the `ErrEndpointIDInUse` special case) to mention the new one, and add the log-level distinction there instead of leaving it at the call sites' default Warn — since an unknown-device refusal is routine/expected (not a bug), Warn (the existing default for anything not `ErrEndpointIDInUse`) is already the right level; no change needed inside `logReconcileFailure` itself beyond the doc comment:

```go
// logReconcileFailure logs a failed identity reconciliation (from either
// handleOnBoardRequest or handleProbeFallback). devices.ErrEndpointIDInUse
// is logged at Error, not Warn, and with wording that says so explicitly:
// it means this connection's endpoint id is already durably bound to a
// different device in usp_agents, which is not a transient condition --
// the agent will retry the same OnBoardRequest forever without an
// operator resolving the collision (final-review finding 6).
// devices.ErrUnknownDevice (the USP agent allowlist's identity gate) is a
// routine, expected refusal -- not an operator-actionable bug -- so it
// keeps the default Warn treatment; both call sites additionally close
// the connection for this specific error (see their own call sites).
// Every other failure (a transient DB error, etc.) also keeps the
// existing Warn treatment, since a retry may well succeed on its own next
// time.
func (h *handler) logReconcileFailure(c mtp.Conn, msg string, err error) {
```

Confirm `handler.go` already imports `"errors"` and `"acs/internal/devices"` (both are used elsewhere in the file already, per `logReconcileFailure`'s existing `errors.Is(err, devices.ErrEndpointIDInUse)` call) — no new imports needed.

- [ ] **Step 7: Write the failing test for the close behavior**

Add to `handler_test.go`:

```go
// TestHandlerOnBoardRequestClosesConnectionForUnknownDevice and
// TestHandlerProbeFallbackClosesConnectionForUnknownDevice cover this
// plan's identity-level gate at the handler layer: an OnBoardRequest (or
// probe fallback) for an identity the store doesn't recognize must close
// the connection, not just fail reconciliation silently.
func TestHandlerOnBoardRequestClosesConnectionForUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore() // deliberately not seeded
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(c.closed) != 1 {
		t.Fatalf("c.closed = %v, want exactly 1 close call for an unknown-device OnBoardRequest", c.closed)
	}
	if h.isReconciled(c) {
		t.Error("connection marked reconciled despite an unknown-device refusal")
	}
}

func TestHandlerProbeFallbackClosesConnectionForUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore() // deliberately not seeded
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	h.OnConnect(c)
	if err := h.probe.start(context.Background(), c); err != nil {
		t.Fatalf("probe.start: %v", err)
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent has %d probe Gets, want 2", len(c.sent))
	}
	id1 := sentMsgID(t, ctrl, agent, c.sent[0])

	params := deviceInfoParams("0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, getResp(id1, params))})

	if len(c.closed) != 1 {
		t.Fatalf("c.closed = %v, want exactly 1 close call for an unknown-device probe fallback", c.closed)
	}
}
```

These reference `c.closed`, a field on `captureConn` — check whether `captureConn` (this file's fake `mtp.Conn`) already records `Close` calls. If it does not yet, add it: find `captureConn`'s definition (likely in `handler_test.go` itself or a shared test-helpers file in this package) and add a `closed []string` field plus a `Close(reason string) error` method appending `reason` to it and returning nil, matching however this fake already implements the rest of the `mtp.Conn` interface (`Send`, `RemoteAddr`, `Endpoint`, `Kind`).

- [ ] **Step 8: Run to verify the tests fail, then pass after Step 6's implementation**

Run: `cd backend && go test ./cmd/uspc/... -run 'ClosesConnectionForUnknownDevice' -v`
Expected: first run (before Step 6, if run out of order) FAILS with "0 close calls, want 1"; after Step 6's `handler.go` change, PASS.

- [ ] **Step 9: Fix `main_test.go`'s seed call**

`TestResetUspAgentsConnectedClearsStaleRows` currently seeds a device via `repo.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")` directly (line 39 as of this plan's writing) — this now fails with `ErrUnknownDevice` since nothing pre-registers it first. Change:

```go
	repo := devices.NewRepository(db)
	d, err := repo.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
```

to:

```go
	repo := devices.NewRepository(db)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := repo.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil); err != nil {
		t.Fatalf("pre-register seed device: %v", err)
	}
	d, err := repo.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
```

Add `"acs/internal/cwmp"` to `main_test.go`'s imports.

- [ ] **Step 10: Update `dispatcher_test.go`'s comment**

Around line 1013-1015, the comment reads `"...as if reconciler.onBoard's own UpsertFromOnBoard/LinkUspAgent had just run."` — update to `"...as if reconciler.onBoard's own ReconcileFromOnBoard/LinkUspAgent had just run."`. No code change (this test manipulates `agentsByEndpointID` directly, never calls the renamed method).

- [ ] **Step 11: Update `main.go`'s doc comment and startup log**

Replace the package doc comment's warning (currently lines 14-17):

```go
// Agent allowlisting does not exist yet: any agent that completes the
// WebSocket subprotocol/query-parameter handshake or publishes to the
// MQTT controller topic is accepted. Do not expose this service to an
// untrusted network until that lands.
```

with:

```go
// Agent allowlisting is two independent gates (design
// docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md):
// network-level (ACS_USP_ALLOWED_CIDRS, empty is permissive) and
// identity-level (an agent's OUI+SerialNumber must already correspond to
// a devices row -- pre-registered via the bulk-import API, a prior CWMP
// Inform, or a prior USP onboarding -- or the connection is refused and
// closed).
```

Replace the startup warning log line (currently `logger.Warn("agent allowlisting is not implemented in this build; do not expose uspc to an untrusted network")`):

```go
	if len(cfg.AllowedCIDRs) == 0 {
		logger.Warn("ACS_USP_ALLOWED_CIDRS is not set: any network can reach this service's listeners. Set it for a production deployment.")
	}
```

(No corresponding warning is needed for the identity gate — it is unconditional, not a configuration choice, so there is nothing to warn about.)

- [ ] **Step 12: Run the full `cmd/uspc` test suite**

Run: `cd backend && go test ./cmd/uspc/... -v 2>&1 | tail -80`
Expected: PASS, every test in the package (real Postgres via `ACS_TEST_POSTGRES_DSN`, same as every prior task this session).

- [ ] **Step 13: Run the whole backend test suite to catch any other stray reference**

Run: `cd backend && go build ./... && go vet ./... && go test ./... 2>&1 | tail -60`
Expected: build clean, vet clean, every package `ok`. If `go build` OOM-crashes the Windows linker (an observed local flake in this environment, unrelated to code correctness), rely on `go vet ./...` and `go test ./...` (both compile the whole tree) as sufficient proof instead of retrying the build.

- [ ] **Step 14: `gofmt`**

Run: `cd backend && gofmt -l .`
Expected: no output.

- [ ] **Step 15: Commit**

```bash
git add backend/cmd/uspc/identity.go backend/cmd/uspc/identity_test.go backend/cmd/uspc/handler.go backend/cmd/uspc/handler_test.go backend/cmd/uspc/main_test.go backend/cmd/uspc/dispatcher_test.go backend/cmd/uspc/main.go
git commit -m "$(cat <<'EOF'
feat(uspc): close the connection on an unknown-identity refusal

Wires devices.ReconcileFromOnBoard's renamed, reject-unless-known
contract through identity.go and handler.go: both identity paths
(OnBoardRequest, probe fallback) now close the connection immediately
on ErrUnknownDevice, mirroring the design spec's stated philosophy for
the symmetric agent-side gate ("an unknown controller is refused
outright, not demoted to an untrusted role").

Both gates (network CIDR, identity) now exist -- main.go's startup
warning and package doc comment are updated to describe them instead
of the old "no allowlist" caveat.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: CI — pre-register test devices, prove the gate against real obuspa

**Files:**
- Modify: `ci/usp/assert-getresp.sh`
- Modify: `ci/usp/assert-job-dispatch.sh`
- Modify: `ci/usp/assert-subscription.sh`
- Create: `ci/usp/assert-allowlist.sh`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `devices.Repository.PreRegister`'s SQL shape (Task 3 doesn't change `PreRegister` itself, but this task's raw-SQL inserts must match the real `devices` table schema Task 3's tests already exercise — `oui_serial`, `manufacturer`, `oui`, `product_class`, `serial_number`, `customer_id`, `tags`, `online_status`, `first_seen_at`, `last_updated_at`).
- Produces: nothing consumed by a later task — this is the last CI-proof task.

- [ ] **Step 1: Read the three existing scripts and the `usp-interop` job in full**

Read `ci/usp/assert-getresp.sh`, `ci/usp/assert-job-dispatch.sh`, `ci/usp/assert-subscription.sh`, and the `usp-interop` job in `.github/workflows/ci.yml` end to end before editing anything — this task's raw-SQL insert must match each script's existing pattern for finding the device id / obuspa endpoint id (established across B-3a/B-3b/B-3c's CI work already on this branch) exactly, not a new invented lookup.

- [ ] **Step 2: Add a pre-registration insert to each of the three existing scripts**

In each of `assert-getresp.sh`, `assert-job-dispatch.sh`, `assert-subscription.sh`, before the step that launches the obuspa container, add a raw `psql` insert seeding the test device's identity, matching the codebase's real `devices` columns and the `oui_serial` natural-key convention (`OUI + "-" + ProductClass + "-" + SerialNumber` if that's what `cwmp.DeviceID.NaturalKey()` produces — verify the exact separator against `backend/internal/cwmp/deviceid.go` before writing the SQL literal, do not guess it):

```bash
# Pre-register the test device's identity ahead of its first USP contact
# -- required since cmd/uspc's identity gate (design
# docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md) now
# refuses an unknown OUI+SerialNumber.
psql "$POSTGRES_DSN" -c "
  INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, online_status, first_seen_at, last_updated_at, management_protocols, tags)
  VALUES (gen_random_uuid(), '<exact NaturalKey() value for the test identity this script already uses>', 'obuspa-ci', '<OUI>', '<ProductClass>', '<SerialNumber>', 'OFFLINE', now(), now(), '{}', '{}')
  ON CONFLICT (oui_serial) DO NOTHING;
"
```

Use each script's own existing test identity literals (check what OUI/ProductClass/SerialNumber value, if any, the script or obuspa's factory-reset config already assumes — obuspa's own `VENDOR_OUI="012345"` compile-time default from `ci/usp/obuspa-websocket.txt`/equivalent files is very likely the OUI to use; find the exact ProductClass/SerialNumber obuspa's factory-reset config declares for `Device.DeviceInfo.ProductClass`/`SerialNumber` rather than inventing new values — these must match what the real agent actually reports in its OnBoardRequest/GetResp, or the pre-registration is for the wrong identity and the gate refuses it anyway).

- [ ] **Step 3: Write `assert-allowlist.sh`**

This proves BOTH gates against real obuspa: (a) an unregistered agent identity is refused and its connection closes; (b) — reuse of what the other three scripts already prove — a pre-registered one is accepted. Mirror `assert-job-dispatch.sh`'s structure (bounded polling, `fail()` helper, log-tail on failure).

```bash
#!/usr/bin/env bash
set -euo pipefail

# assert-allowlist.sh proves the USP agent allowlist's identity-level gate
# against a real obuspa instance: an agent whose identity was never
# pre-registered is refused and its connection is closed by cmd/uspc, not
# silently onboarded. Design:
# docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md.
#
# Usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn>

USPC_LOG="${1:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn>}"
POSTGRES_DSN="${2:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn>}"

fail() {
  echo "FAIL: $1" >&2
  echo "--- uspc log tail ---" >&2
  tail -n 100 "$USPC_LOG" >&2 || true
  exit 1
}

# The unregistered identity this obuspa instance's factory-reset config
# reports -- deliberately never inserted into devices by this script,
# unlike assert-getresp.sh/assert-job-dispatch.sh/assert-subscription.sh's
# now-pre-registered identity (Step 2). Confirm this container's
# factory-reset config actually declares this OUI/ProductClass/
# SerialNumber combination (read it before hardcoding the value below) --
# use whatever this CI job's SECOND, deliberately-unregistered obuspa
# instance is configured to report.
UNREGISTERED_OUI_SERIAL="<NaturalKey() value NOT present in devices -- confirm via the second obuspa container's factory-reset config>"

echo "Waiting for uspc to log a refusal for the unregistered agent..."
for i in $(seq 1 60); do
  if grep -q "failed to reconcile onboard request" "$USPC_LOG" 2>/dev/null && grep -q "agent identity not pre-registered\|ErrUnknownDevice\|unknown device" "$USPC_LOG" 2>/dev/null; then
    echo "PASS: uspc logged a refusal for the unregistered agent"
    break
  fi
  if [ "$i" -eq 60 ]; then
    fail "uspc never logged an unknown-device refusal within 60s"
  fi
  sleep 1
done

# Confirm no devices row was created for the unregistered identity --
# the refusal must not have silently onboarded it anyway.
COUNT=$(psql "$POSTGRES_DSN" -t -c "SELECT count(*) FROM devices WHERE oui_serial = '$UNREGISTERED_OUI_SERIAL';" | tr -d '[:space:]')
if [ "$COUNT" != "0" ]; then
  fail "devices row count for the unregistered identity = $COUNT, want 0 (refusal must not create a row)"
fi

echo "assert-allowlist.sh: PASS"
```

Fill in `UNREGISTERED_OUI_SERIAL` and the exact log-message substrings (`grep` patterns above use the exact wording from Task 4's `logReconcileFailure` call and the `ErrUnknownDevice` sentinel's error text — verify both literally against the real `handler.go`/`usp.go` source from Tasks 3-4 before finalizing this script, don't guess the wording) once this task is actually being executed against the real, already-committed Task 3/4 code.

`chmod +x ci/usp/assert-allowlist.sh` after writing it.

- [ ] **Step 4: Wire a second, deliberately-unregistered obuspa instance and `assert-allowlist.sh` into `ci.yml`**

In the `usp-interop` job's WebSocket step (the same step already reused for `assert-job-dispatch.sh` and `assert-subscription.sh`), after the existing pre-registration insert (Step 2) and the existing obuspa container start, add:
- A second obuspa container (different factory-reset config file, or the same image with a different `SerialNumber` override if obuspa supports one via an env var or CLI flag — check obuspa's real startup options before assuming this; if no such override exists, create `ci/usp/obuspa-websocket-unregistered.txt` as a copy of the existing factory-reset config with only the `SerialNumber`/`ProductClass` values changed to something this script deliberately never pre-registers) connecting to the same `cmd/uspc` instance.
- A call to `ci/usp/assert-allowlist.sh "$USPC_LOG" "$POSTGRES_DSN"` after that second container has had time to attempt its connection.

- [ ] **Step 5: Validate syntax**

Run: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"`
Run: `bash -n ci/usp/assert-allowlist.sh && bash -n ci/usp/assert-getresp.sh && bash -n ci/usp/assert-job-dispatch.sh && bash -n ci/usp/assert-subscription.sh`
Expected: `YAML OK`, all four scripts pass `bash -n` with no output.

- [ ] **Step 6: Check `docker ps` and run locally if reachable**

Run: `docker ps`
If Docker is reachable with real `--network host` support, run the full `usp-interop` job's steps locally and record the output. If not (the established Windows Docker limitation for this whole programme — no `--network host` support), say so plainly rather than claiming it ran.

- [ ] **Step 7: Commit**

```bash
git add ci/usp/assert-getresp.sh ci/usp/assert-job-dispatch.sh ci/usp/assert-subscription.sh ci/usp/assert-allowlist.sh .github/workflows/ci.yml
git commit -m "$(cat <<'EOF'
ci: prove the USP agent allowlist against real obuspa

Every existing interop script now pre-registers its test device ahead
of obuspa's first connection (the identity gate refuses anything
unregistered). A new script and a second, deliberately-unregistered
obuspa instance prove the refusal itself: an unknown identity is
logged as refused and never onboarded, not silently accepted.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Deployment documentation

**Files:**
- Modify: `README.md`
- Modify: `deployment-testing-onboarding-guide.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing (last task in the plan).

- [ ] **Step 1: Update `README.md`'s services table**

Replace the `uspc` row (currently: `... Wiring only — probes each connecting agent, does not yet dispatch real jobs. Has no agent allowlist yet — do not expose it to an untrusted network.` — note this text is already stale relative to B-3a/B-3b/B-3c's real dispatch/subscription work merged earlier this session and should be corrected here too, not just the allowlist clause):

```
| USP controller | `backend/cmd/uspc` | TR-369/USP controller: WebSocket (`:9877`) and MQTT (`:1883`) MTPs, `/healthz`/`/readyz`/`/metrics` on `:8092`. Dispatches queued jobs, reconciles subscriptions, and gates connections with a network CIDR allowlist (`ACS_USP_ALLOWED_CIDRS`) plus an identity allowlist (the agent must already be a known `devices` row — see below). |
```

- [ ] **Step 2: Add `ACS_USP_ALLOWED_CIDRS` to the recommended hardening variables list**

Near the existing `ACS_DEVICE_NET_ALLOWED_CIDRS` mention (README.md, "Recommended hardening variables" section), add:

```
`ACS_USP_ALLOWED_CIDRS` (network-level allowlist for the USP
controller's WebSocket/MQTT listeners; identity-level allowlisting is
unconditional — pre-register a device via the bulk-import API before
its first USP contact),
```

- [ ] **Step 3: Update `deployment-testing-onboarding-guide.md`'s `cmd/uspc` section**

Replace:

```
# cmd/uspc (TR-369/USP controller: WebSocket + MQTT MTPs)
# NOTE: cmd/uspc has no agent allowlist yet -- any agent that completes
# the handshake is accepted. Do not expose it to an untrusted network.
```

with:

```
# cmd/uspc (TR-369/USP controller: WebSocket + MQTT MTPs)
# Two independent allowlist gates:
#   - Network-level: ACS_USP_ALLOWED_CIDRS (comma-separated CIDRs,
#     e.g. "10.0.0.0/8,192.168.1.0/24"). Empty/unset is permissive --
#     set this for a production deployment.
#   - Identity-level: unconditional, no config flag. An agent's
#     OUI+SerialNumber must already correspond to a devices row before
#     its first USP contact -- pre-register it via the bulk-import API
#     (same PreRegister path CWMP fleet onboarding already uses), or
#     let it connect via CWMP first if it's a dual-stack device. An
#     unrecognized identity is refused and the connection closed.
ACS_USP_ALLOWED_CIDRS (optional, comma-separated CIDR list, empty is
  permissive -- see above),
```

(keep the existing `ACS_USP_CONTROLLER_ID` and other already-documented variables below this, unchanged).

- [ ] **Step 4: Commit**

```bash
git add README.md deployment-testing-onboarding-guide.md
git commit -m "$(cat <<'EOF'
docs: document the USP agent allowlist's two gates

Replaces the "no allowlist, don't expose to an untrusted network"
caveat with real operator guidance now that both gates exist:
ACS_USP_ALLOWED_CIDRS for network-level, and pre-registration (via the
existing bulk-import API) for identity-level.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**1. Spec coverage.**

| Design spec section | Task |
|---|---|
| S2.1 network-level CIDR allowlist, both MTPs | 1, 2 |
| S2.2 identity-level known-device gate, both identity paths | 3, 4 |
| S2.2 close connection on refusal | 4 |
| S2.3 no disruption to the existing fleet | 3 (update-only contract preserves every existing row) |
| S3 `data_model_root` fix | 3 |
| S4 CI impact (pre-register + prove the refusal) | 5 |
| S5 deployment documentation | 6 |
| S6 testing (unit: filtering listener, MQTT hook, `ReconcileFromOnBoard`, close-on-error; CI: real obuspa) | 1, 3, 4, 5 |
| S7 out of scope (mTLS, allowlist-specific UI, rate limiting, retroactive revocation) | not built — confirmed absent from every task above |

**2. Placeholder scan.** Two intentional inline notes (Task 3 Step 2's `var _ = sql.ErrNoRows` line, Task 3 Step 4's `var _ = uuid.New` line) are explicitly flagged as scaffolding to delete before commit, with the exact condition for deleting them — not left ambiguous. Task 5's `assert-allowlist.sh` has two values (`UNREGISTERED_OUI_SERIAL`, the grep patterns) that depend on the real committed Task 3/4 source and the real obuspa factory-reset config, which don't exist as literal strings until those tasks run — each is called out explicitly as "verify against the real source, don't guess" rather than left as a bare TBD, consistent with this whole programme's established CI-task pattern (every prior real-agent CI task in this session's B-2/B-3a/B-3b/B-3c work had to do the same live verification against a config file or generated protobuf that didn't exist until an earlier task landed).

**3. Type consistency.** `devices.Repository.ReconcileFromOnBoard(ctx, oui, productClass, serialNumber string) (*Device, error)` and `devices.ErrUnknownDevice` (Task 3) are consumed by exact name in Task 4's `identityStore` interface, `reconciler.onBoard`/`fromProbeFallback`, and `handler.go`'s `errors.Is` checks. `mtp.WebSocketConfig.AllowedCIDRs`/`mtp.MQTTConfig.AllowedCIDRs []*net.IPNet` (Task 1) are consumed by exact name in Task 2's `newTransports`. `serviceConfig.AllowedCIDRs` (Task 2) doesn't feed into Task 3/4 (identity gate is unconditional, no config coupling) — confirmed no task incorrectly assumes otherwise.

**Ordering.** 1 and 3 have no shared files and could run in parallel; this plan lists them sequentially (1 → 2 → 3 → 4 → 5 → 6) for a single controller running this via subagent-driven-development, since 2 needs 1 and 4 needs 3, and interleaving two independent chains adds coordination complexity for no real benefit at this plan's size. 5 needs 2 (config wiring, so CI's env var actually does something) and 4 (the log-message text `assert-allowlist.sh` greps for). 6 needs 2 and 4 (the real config var name and behavior it documents).
