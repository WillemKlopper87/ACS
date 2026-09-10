# USP Transport and Connectivity Implementation Plan (B-2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put a transport under the B-1 protocol core so a real USP agent can connect over WebSocket or MQTT and answer a `Get`, proven against the Broadband Forum reference agent in CI.

**Architecture:** A `Transport` interface in `internal/usp/mtp` with two implementations — a WebSocket server on `coder/websocket` and an embedded MQTT broker on `mochi-mqtt` — feeding a `Registry` that maps Endpoint ID to one live connection. `cmd/uspc` is wiring only: fail-closed config, both listeners, health and metrics, graceful shutdown, and an on-connect `Get` probe that is the interop evidence. No database, no job queue, no identity reconciliation — those are B-3.

**Tech Stack:** Go 1.26; `github.com/coder/websocket` v1.8.15 (zero transitive deps); `github.com/mochi-mqtt/server/v2` v2.7.9 embedded (links four modules: itself, `rs/xid`, `gopkg.in/yaml.v3`, and `gorilla/websocket` via its listeners package — the 49 modules in its graph are optional hooks this code never imports). The reference agent `obuspa` runs in CI from its own `ci/Dockerfile`.

**Spec:** [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) — this plan implements §4.1's `mtp` package and `cmd/uspc`, §6.1's connection registry, §8 (security and trust, transport half), §9 (the obuspa acceptance gate), and settles open question 2 (controller EndpointID). Read both.

## How this plan is written — read before executing

B-1's execution found that five of six tasks needed a fix round for one cause: the plan shipped literal implementation code alongside prose intent, the two disagreed, and implementers correctly followed the code. This plan therefore:

- **Gives tests as literal code.** Tests are the contract; write them exactly as shown.
- **Gives implementation as a contract**, not a code block: what each function must do, the decisions already made, and the specific library calls that are non-obvious. You write the code.
- **Names a test for every requirement.** Each task's **Checklist** maps requirement → test. If you find a requirement with no test, that is a plan bug — report it, do not silently skip it.
- **Lists non-textual file properties as explicit steps** (executable bits, line endings), because a pasted block cannot carry them.

## Programme context

| Plan | Deliverable | Depends on |
|---|---|---|
| B-1 | Protocol core — done. | — |
| **B-2** (this) | Transports, registry, `cmd/uspc`, obuspa interop in CI. Deliverable: a real agent connects on each MTP and answers a `Get`. | B-1 |
| B-3 | `OnBoardRequest` identity reconciliation, `usp_agents` table, job→USP dispatch, subscriptions, Notify routing, agent allowlist. | B-2, sub-project A |

## Global Constraints

- Go module is `acs`; Go directive `go 1.26.6`. Do not raise it.
- **Exactly two new direct dependencies are permitted:** `github.com/coder/websocket v1.8.15` and `github.com/mochi-mqtt/server/v2 v2.7.9`. Task 3 adds the first, Task 4 the second. `go mod tidy` may add indirect lines for what those two require; no other direct require may appear. Verify with `git diff go.mod` before every commit.
- `internal/usp/...` must not import `acs/internal/devices`, `acs/internal/jobs`, `acs/internal/store`, `acs/internal/sessions`, `acs/internal/cwmp`, or `acs/cmd/...`. B-1's walk-based boundary test enforces this over the whole tree, so `internal/usp/mtp` is guarded automatically — do not weaken that test.
- `cmd/uspc` is the only place protocol meets process: it may import `internal/usp/...`, `internal/config`, `internal/observability`. It must not import `internal/devices`, `internal/jobs` or `internal/store` in this plan — B-3 adds those. Task 5's test enforces it.
- Every service **fails closed**: a missing, placeholder (`change-me`), or too-short secret is a fatal startup error. Use `internal/config` exactly as `cmd/bssadapter/main.go` does.
- WebSocket wire contract (spec §3.4 and the USP WebSocket binding): subprotocol `v1.usp` is mandatory both ways (R-WS.9, R-WS.11, R-WS.12a); Records travel as **binary** frames (R-WS.14); fragmented frames must be accepted (R-WS.14b); an undecodable Record closes the connection with a Close frame (R-WS.16); the agent identifies itself by an `eid=` query parameter on the handshake URI (R-WS.10b), percent-encoded with `%` itself encoded (R-WS.10c); at most one open session per endpoint pair (R-WS.4).
- MQTT wire contract (spec §3.4): MQTT v5 agents carry reply-to in the `Response Topic` property and content type `usp.msg`; MQTT v3.1.1 agents publish to `<controller-topic>/reply-to=<agent-topic with "/" escaped as %2F>`. Both must be supported.
- The controller emits `Record.version "1.3"` and accepts 1.0–1.3 — already true via B-1; do not change it.
- Plaintext MTPs are permitted **only** when `ACS_USP_ALLOW_PLAINTEXT=true`; CI sets it, production must not. TLS is the default posture.
- Before every commit, from `backend/`: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.
- Commit message style: `type(scope): summary`, imperative mood. End each commit message with:
  `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`

## Decisions settled here

**Controller EndpointID (spec open question 2).** `self::` + the value of `ACS_USP_CONTROLLER_ID`, which is a required, fail-closed setting of at least 8 bytes matching `^[A-Za-z0-9._-]+$`. A hostname is not used: it changes across restarts and replicas, and the agent's controller table stores this id verbatim, so a drifting id silently orphans every agent.

**Agent allowlisting is B-3, not here.** This plan accepts any `from_id` at the transport layer and keys the registry by it. The DB-backed allowlist (spec §8) needs `usp_agents`, which is B-3. Consequence, stated plainly: **`cmd/uspc` as built by this plan must not be exposed to an untrusted network** — it is a lab and CI service until B-3 lands. Task 5 logs this at startup.

**Both transports accept plaintext under a flag.** obuspa in CI runs without certificates (spec §3.4: plaintext gives full access with zero cert setup, and the real gate is the endpoint-id allowlist). Production keeps TLS on by default.

**The interop probe is a `Get`, sent by the controller on connect.** The deliverable is "an agent answers a `Get`". On every new registry entry `cmd/uspc` sends `Get(["Device.DeviceInfo."], maxDepth 1)` and logs the `GetResp`. B-3 replaces this probe with real dispatch; it exists here to make the CI assertion concrete.

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/usp/record.go` (modify) | `DecodedRecord` gains the record type and disconnect details — B-1 deferred this to B-2 because the MTP layer needs it. |
| `backend/internal/usp/mtp/transport.go` | `Transport`, `Conn`, `Inbound`, `Handler` — the interface every MTP implements. |
| `backend/internal/usp/mtp/registry.go` | `Registry`: one live `Conn` per Endpoint ID; replace-on-reconnect closes the old one. |
| `backend/internal/usp/mtp/websocket.go` | WebSocket server transport on `coder/websocket`. |
| `backend/internal/usp/mtp/mqtt.go` | Embedded MQTT broker transport on `mochi-mqtt`, with v5 and v3.1.1 reply-to. |
| `backend/internal/usp/mtp/mqtt_topics.go` | Pure helpers: reply-to derivation, `%2F` escaping — unit-tested without a broker. |
| `backend/cmd/uspc/main.go` | Wiring only: config, listeners, registry, probe, health, metrics, shutdown. |
| `backend/cmd/uspc/boundary_test.go` | `cmd/uspc` imports no domain package in this plan. |
| `backend/Dockerfile.uspc` | Same shape as `Dockerfile.bssadapter`. |
| `infra/docker-compose.yml` (modify) | `uspc` under the `containerized` profile. |
| `.github/workflows/ci.yml` (modify) | New `usp-interop` job: obuspa on WebSocket and on MQTT, each answering a `Get`. |
| `ci/usp/` | obuspa factory-reset configs for both MTPs, and the assertion script. |

---

### Task 1: Expose the record type from `DecodeRecord`

**Why:** B-1's final review recorded that `DecodeRecord` collapses a WebSocket connect record, a `DisconnectRecord` (and its reason), a `SessionContextRecord`, and an empty payload into one `ErrNoPayload`. The MTP layer must tell them apart: a disconnect carries a reason worth logging, and a session-context record is an unsupported feature to refuse, not an empty message.

**Files:**
- Modify: `backend/internal/usp/record.go`
- Modify: `backend/internal/usp/record_test.go`

**Interfaces:**
- Consumes: B-1's `DecodeRecord`, `DecodedRecord`, `ErrNoPayload`.
- Produces:
  - `type RecordType int` with constants `RecordNoSessionContext`, `RecordSessionContext`, `RecordWebSocketConnect`, `RecordMQTTConnect`, `RecordSTOMPConnect`, `RecordUDSConnect`, `RecordDisconnect`, `RecordUnknown`, and a `String()` method.
  - `DecodedRecord` gains `Type RecordType`, `DisconnectReason string`, `DisconnectCode uint32`.
  - `var ErrSessionContextUnsupported = errors.New("USP session context records are not supported")`.
  - **Contract change:** `DecodeRecord` now returns a **non-nil** `*DecodedRecord` alongside `ErrNoPayload` for connect and disconnect records, with `Type` set, so a caller can `errors.Is(err, ErrNoPayload)` and then inspect `rec.Type`. For a `SessionContextRecord` it returns `ErrSessionContextUnsupported` (and a non-nil record with `Type` set). All other error paths still return a nil record.

**Checklist (requirement → test):**
| Requirement | Test |
|---|---|
| Connect records report their type alongside `ErrNoPayload` | `TestDecodeRecordConnectTypes` |
| Disconnect reason and code are surfaced | `TestDecodeRecordDisconnectCarriesReason` |
| Session context is refused distinctly | `TestDecodeRecordSessionContextUnsupported` |
| Existing `ErrNoPayload` for empty `NoSessionContext` still holds and reports `RecordNoSessionContext` | `TestDecodeRecordNoPayload` (existing, extended) |
| Round-trip and validation tests from B-1 still pass unchanged | the existing suite |

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/usp/record_test.go`:

```go
func TestDecodeRecordConnectTypes(t *testing.T) {
	cases := map[string]struct {
		rt   any
		want RecordType
	}{
		"websocket": {&uspproto.Record_WebsocketConnect{WebsocketConnect: &uspproto.WebSocketConnectRecord{}}, RecordWebSocketConnect},
		"mqtt":      {&uspproto.Record_MqttConnect{MqttConnect: &uspproto.MQTTConnectRecord{Version: uspproto.MQTTConnectRecord_V5, SubscribedTopic: "/usp/agent"}}, RecordMQTTConnect},
		"stomp":     {&uspproto.Record_StompConnect{StompConnect: &uspproto.STOMPConnectRecord{}}, RecordSTOMPConnect},
		"uds":       {&uspproto.Record_UdsConnect{UdsConnect: &uspproto.UDSConnectRecord{}}, RecordUDSConnect},
	}
	for name, c := range cases {
		rec := &uspproto.Record{Version: RecordVersion, ToId: string(testController), FromId: string(testAgent), PayloadSecurity: uspproto.Record_PLAINTEXT}
		switch v := c.rt.(type) {
		case *uspproto.Record_WebsocketConnect:
			rec.RecordType = v
		case *uspproto.Record_MqttConnect:
			rec.RecordType = v
		case *uspproto.Record_StompConnect:
			rec.RecordType = v
		case *uspproto.Record_UdsConnect:
			rec.RecordType = v
		}
		got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
		if !errors.Is(err, ErrNoPayload) {
			t.Errorf("%s: err = %v, want ErrNoPayload", name, err)
		}
		if got == nil {
			t.Fatalf("%s: record is nil; a connect record must be returned so its type can be read", name)
		}
		if got.Type != c.want {
			t.Errorf("%s: Type = %v, want %v", name, got.Type, c.want)
		}
		if got.From != testAgent {
			t.Errorf("%s: From = %q, want %q -- the sender must be known even without a payload", name, got.From, testAgent)
		}
	}
}

func TestDecodeRecordDisconnectCarriesReason(t *testing.T) {
	rec := &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_Disconnect{Disconnect: &uspproto.DisconnectRecord{Reason: "agent shutting down", ReasonCode: 7005}},
	}
	got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
	if !errors.Is(err, ErrNoPayload) {
		t.Fatalf("err = %v, want ErrNoPayload", err)
	}
	if got == nil || got.Type != RecordDisconnect {
		t.Fatalf("got %+v, want a RecordDisconnect", got)
	}
	if got.DisconnectReason != "agent shutting down" || got.DisconnectCode != 7005 {
		t.Errorf("disconnect detail = (%q, %d), want (agent shutting down, 7005)", got.DisconnectReason, got.DisconnectCode)
	}
}

// Session context exists for segmentation and end-to-end encryption, both
// out of scope and both refused by the reference agent. It must be
// refused distinctly, not mistaken for an empty message.
func TestDecodeRecordSessionContextUnsupported(t *testing.T) {
	rec := &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_SessionContext{SessionContext: &uspproto.SessionContextRecord{SessionId: 1, Payload: [][]byte{[]byte("x")}}},
	}
	got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
	if !errors.Is(err, ErrSessionContextUnsupported) {
		t.Errorf("err = %v, want ErrSessionContextUnsupported", err)
	}
	if errors.Is(err, ErrNoPayload) {
		t.Error("a session-context record must not be reported as ErrNoPayload; it carries a payload we refuse")
	}
	if got == nil || got.Type != RecordSessionContext {
		t.Errorf("got %+v, want Type RecordSessionContext", got)
	}
}

func TestRecordTypeString(t *testing.T) {
	if RecordDisconnect.String() == "" || RecordUnknown.String() == "" {
		t.Error("RecordType.String() must never be empty; it is logged")
	}
	if RecordType(999).String() == RecordUnknown.String() && RecordUnknown.String() == "" {
		t.Error("unmapped types must render")
	}
}
```

Then extend the existing `TestDecodeRecordNoPayload`: after the existing `errors.Is(err, ErrNoPayload)` assertion, assert the returned record is non-nil and that the `"empty_no_session_context"` case reports `Type == RecordNoSessionContext`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestDecodeRecord|TestRecordType' -v`

Expected: compile FAIL — `undefined: RecordType`, `undefined: ErrSessionContextUnsupported`, and the existing no-payload test failing on a nil record.

- [ ] **Step 3: Implement the contract**

In `record.go`: add `RecordType`, its constants and `String()`; add the three fields to `DecodedRecord`; add `ErrSessionContextUnsupported`. In `DecodeRecord`, after the version / security / recipient checks pass, build the `DecodedRecord` with `From`, `To`, `Version` first, then switch on the oneof: `NoSessionContext` → set `Type`, extract payload, return `ErrNoPayload` **with the record** if empty; `SessionContext` → set `Type`, return the record and `ErrSessionContextUnsupported`; each connect type → set `Type`, return the record and `ErrNoPayload`; `Disconnect` → set `Type`, `DisconnectReason`, `DisconnectCode`, return the record and `ErrNoPayload`; anything else → `RecordUnknown` and `ErrNoPayload`. Keep the doc comment honest: state that a non-nil record can accompany an error and what the caller should do with it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/... -v`

Expected: all PASS, including every B-1 test unchanged.

- [ ] **Step 5: Full backend checks, then commit**

Run: `gofmt -l . && go vet ./... && go test ./...` — clean.

```bash
git add internal/usp/record.go internal/usp/record_test.go
git commit -F - <<'EOF'
feat(usp): surface the record type from DecodeRecord

The MTP layer needs to tell a connect record from a disconnect (which
carries a reason worth logging) from a session-context record (an
unsupported feature to refuse, not an empty message). DecodeRecord now
returns a typed, non-nil record alongside ErrNoPayload for lifecycle
records, and a distinct ErrSessionContextUnsupported.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 2: The transport interface and the connection registry

**Files:**
- Create: `backend/internal/usp/mtp/transport.go`, `backend/internal/usp/mtp/registry.go`
- Test: `backend/internal/usp/mtp/registry_test.go`

**Interfaces:**
- Consumes: `usp.EndpointID`.
- Produces (package `mtp`):
  - `type Kind string` with `KindWebSocket Kind = "WebSocket"` and `KindMQTT Kind = "MQTT"` — the exact strings the reference agent uses in `Device.LocalAgent.MTP.{i}.Protocol`.
  - `type Conn interface { Endpoint() usp.EndpointID; Kind() Kind; Send(ctx context.Context, record []byte) error; Close(reason string) error; RemoteAddr() string }`
  - `type Inbound struct { Conn Conn; Record []byte; ReceivedAt time.Time }`
  - `type Handler interface { OnConnect(Conn); OnRecord(Inbound); OnDisconnect(Conn, error) }`
  - `type Transport interface { Kind() Kind; Start(ctx context.Context, h Handler) error; Stop(ctx context.Context) error; Addr() string }`
  - `type Registry struct{ ... }`, `func NewRegistry() *Registry`, `func (r *Registry) Add(c Conn) (replaced Conn)`, `func (r *Registry) Remove(c Conn) bool`, `func (r *Registry) Get(id usp.EndpointID) (Conn, bool)`, `func (r *Registry) Len() int`, `func (r *Registry) Each(fn func(Conn))`.

**Contract for `Registry`:** at most one `Conn` per Endpoint ID (R-WS.4 generalised to both MTPs). `Add` on an id that already has a live conn replaces it and returns the old conn so the caller can close it with a reason. `Remove` is a no-op returning false if the registry holds a *different* conn for that id — a stale disconnect from a replaced connection must not evict its replacement. All methods are safe for concurrent use.

**Checklist:**
| Requirement | Test |
|---|---|
| One conn per id; second `Add` replaces and returns the old | `TestRegistryReplaceOnReconnect` |
| Stale `Remove` of a replaced conn does not evict the replacement | `TestRegistryStaleRemoveIsNoop` |
| `Get` on unknown id is `(nil, false)` | `TestRegistryGetUnknown` |
| Concurrent `Add`/`Remove`/`Get` race-free | `TestRegistryConcurrent` (run with `-race`) |

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/usp/mtp/registry_test.go`:

```go
package mtp

import (
	"context"
	"sync"
	"testing"

	"acs/internal/usp"
)

// fakeConn is the minimal Conn a registry test needs. It records whether
// Close was called and with what reason, which is how a caller is
// expected to treat the conn Add hands back on replacement.
type fakeConn struct {
	id     usp.EndpointID
	kind   Kind
	closed string
	mu     sync.Mutex
}

func (f *fakeConn) Endpoint() usp.EndpointID { return f.id }
func (f *fakeConn) Kind() Kind               { return f.kind }
func (f *fakeConn) RemoteAddr() string       { return "test" }
func (f *fakeConn) Send(context.Context, []byte) error { return nil }
func (f *fakeConn) Close(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = reason
	return nil
}

const agentA = usp.EndpointID("os::012345-AAAA")

func TestRegistryReplaceOnReconnect(t *testing.T) {
	r := NewRegistry()
	first := &fakeConn{id: agentA, kind: KindWebSocket}
	if old := r.Add(first); old != nil {
		t.Fatalf("first Add returned a replaced conn %v, want nil", old)
	}
	second := &fakeConn{id: agentA, kind: KindMQTT}
	old := r.Add(second)
	if old != first {
		t.Fatalf("second Add returned %v, want the first conn so the caller can close it", old)
	}
	got, ok := r.Get(agentA)
	if !ok || got != second {
		t.Errorf("Get = (%v, %v), want the replacement", got, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1: one conn per endpoint id", r.Len())
	}
}

// A disconnect callback from a connection that has already been replaced
// must not evict its replacement -- otherwise a slow close on the old
// socket knocks the live one out of the registry.
func TestRegistryStaleRemoveIsNoop(t *testing.T) {
	r := NewRegistry()
	first := &fakeConn{id: agentA, kind: KindWebSocket}
	second := &fakeConn{id: agentA, kind: KindWebSocket}
	r.Add(first)
	r.Add(second)
	if removed := r.Remove(first); removed {
		t.Error("Remove(first) reported true, but first was already replaced and must be a no-op")
	}
	if got, ok := r.Get(agentA); !ok || got != second {
		t.Errorf("after stale Remove, Get = (%v, %v), want the live replacement", got, ok)
	}
	if removed := r.Remove(second); !removed {
		t.Error("Remove(second) reported false for the live conn")
	}
	if _, ok := r.Get(agentA); ok {
		t.Error("conn still present after removing the live one")
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := NewRegistry()
	if c, ok := r.Get("os::nobody-here"); ok || c != nil {
		t.Errorf("Get on unknown id = (%v, %v), want (nil, false)", c, ok)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := usp.EndpointID("os::012345-" + string(rune('A'+i%5)))
			c := &fakeConn{id: id, kind: KindWebSocket}
			if old := r.Add(c); old != nil {
				_ = old.Close("replaced")
			}
			r.Get(id)
			r.Each(func(Conn) {})
			r.Remove(c)
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/mtp/ -v`

Expected: compile FAIL — the package does not exist yet.

- [ ] **Step 3: Implement the contract**

Create `transport.go` with the types in **Produces**, each with a doc comment stating its role in the *why* voice the rest of `internal/usp` uses. Create `registry.go` with a mutex-guarded `map[usp.EndpointID]Conn`. `Add` swaps under the lock and returns the previous value. `Remove` compares identity (`existing == c`) under the lock before deleting. `Each` copies the values under the lock, then calls `fn` outside it, so a callback that closes a conn cannot deadlock the registry.

- [ ] **Step 4: Run tests to verify they pass, including the race detector**

Run: `go test -race ./internal/usp/mtp/ -v` (if `-race` is unavailable locally on Windows without CGO, run without it, state that in the report, and rely on CI's `-race` job).

Expected: all PASS.

- [ ] **Step 5: Confirm the boundary guard covers the new package**

Run: `go test ./internal/usp/ -run 'TestUSPImports|TestUSPTree' -v`

Expected: PASS, and the walk's package list now includes `acs/internal/usp/mtp`. B-1's guard is walk-based precisely so this happens without editing it.

- [ ] **Step 6: Full backend checks, then commit**

```bash
git add internal/usp/mtp/transport.go internal/usp/mtp/registry.go internal/usp/mtp/registry_test.go
git commit -F - <<'EOF'
feat(usp/mtp): transport interface and connection registry

One live connection per endpoint id, on every transport. A reconnect
replaces the old connection and hands it back so the caller can close it
with a reason; a stale disconnect from the replaced connection is a
no-op, so a slow close on the old socket cannot evict the live one.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 3: WebSocket transport

**Files:**
- Create: `backend/internal/usp/mtp/websocket.go`
- Test: `backend/internal/usp/mtp/websocket_test.go`
- Modify: `backend/go.mod`, `backend/go.sum` (adds `github.com/coder/websocket v1.8.15`)

**Interfaces:**
- Consumes: `Transport`, `Conn`, `Handler`, `Inbound`, `Kind` from Task 2; `usp.EndpointID`, `usp.PercentDecodeUSP`.
- Produces: `type WebSocketConfig struct { Addr string; Path string; TLS *tls.Config; AllowPlaintext bool; PingInterval time.Duration; MaxRecordBytes int64 }`, `func NewWebSocket(cfg WebSocketConfig, log *slog.Logger) (*WebSocket, error)`, and `*WebSocket` implementing `Transport`.

**Contract:**
- `Start` serves HTTP on `cfg.Addr` at `cfg.Path` (default `/usp`). With `TLS == nil` and `!AllowPlaintext`, `NewWebSocket` returns an error — plaintext is opt-in.
- The handler calls `websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"v1.usp"}})`. If `conn.Subprotocol() != "v1.usp"` after accept, close with `StatusProtocolError` — R-WS.12a says a server must not establish a session without the subprotocol, and `Accept` will negotiate it only if the client offered it.
- The agent's Endpoint ID comes from the `eid` query parameter (R-WS.10b). Decode it with `usp.PercentDecodeUSP` (R-WS.10c: `%` itself is encoded). A missing or undecodable `eid` is rejected with HTTP 400 before upgrade.
- Read loop: `conn.Read(ctx)` in a goroutine for the life of the connection — `coder/websocket` answers pings only while something is reading (R-WS.13). A frame that is not `MessageBinary` closes with `StatusUnsupportedData` (R-WS.14). Fragmentation is handled by the library (R-WS.14b). Each binary message is delivered as one `Inbound` to `Handler.OnRecord`; the transport does **not** decode it — that is `cmd/uspc`'s job — but if `cmd/uspc` reports it undecodable by calling `Conn.Close`, the transport closes with `StatusUnsupportedData` and reason (R-WS.16).
- `Send` writes one `MessageBinary` frame. `Close(reason)` sends a Close frame with `StatusNormalClosure` and the reason, then stops the read loop.
- `Handler.OnConnect` fires after a successful accept and eid parse; `OnDisconnect` fires exactly once when the read loop ends, with the terminal error (nil on clean close).
- `MaxRecordBytes` caps `conn.SetReadLimit`; default 5 MB, matching the reference agent's frame cap.
- `Addr()` returns the bound address after `Start`, so a test can bind `:0`.

**Checklist:**
| Requirement | Test |
|---|---|
| `v1.usp` negotiated; a client not offering it is refused | `TestWebSocketRequiresSubprotocol` |
| `eid` parsed and percent-decoded from the query | `TestWebSocketEndpointFromQuery` |
| Missing `eid` → 400 before upgrade | `TestWebSocketMissingEIDRejected` |
| Binary frame delivered as `Inbound` with the right `Conn` | `TestWebSocketDeliversBinaryRecord` |
| Text frame closes the connection | `TestWebSocketRejectsTextFrame` |
| `Send` reaches the client as a binary frame | `TestWebSocketSend` |
| `OnDisconnect` fires exactly once on client close | `TestWebSocketDisconnectOnce` |
| Plaintext refused unless allowed | `TestWebSocketPlaintextRequiresOptIn` |

- [ ] **Step 1: Add the dependency**

Run, from `backend/`: `go get github.com/coder/websocket@v1.8.15 && go mod tidy`

Then: `git diff go.mod` must show exactly one new direct require. Record the diff in your report.

- [ ] **Step 2: Write the failing tests**

Create `backend/internal/usp/mtp/websocket_test.go`. Use `coder/websocket`'s client side (`websocket.Dial`) against a transport started on `127.0.0.1:0`. A `recordingHandler` collects `OnConnect`/`OnRecord`/`OnDisconnect` calls behind a mutex with a channel per event so tests can wait with a timeout rather than sleep.

```go
package mtp

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"acs/internal/usp"

	"github.com/coder/websocket"
)

type recordingHandler struct {
	mu          sync.Mutex
	connects    []Conn
	records     []Inbound
	disconnects []Conn
	connected   chan struct{}
	received    chan struct{}
	disconnected chan struct{}
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{
		connected: make(chan struct{}, 16), received: make(chan struct{}, 16), disconnected: make(chan struct{}, 16),
	}
}
func (h *recordingHandler) OnConnect(c Conn) {
	h.mu.Lock(); h.connects = append(h.connects, c); h.mu.Unlock(); h.connected <- struct{}{}
}
func (h *recordingHandler) OnRecord(in Inbound) {
	h.mu.Lock(); h.records = append(h.records, in); h.mu.Unlock(); h.received <- struct{}{}
}
func (h *recordingHandler) OnDisconnect(c Conn, _ error) {
	h.mu.Lock(); h.disconnects = append(h.disconnects, c); h.mu.Unlock(); h.disconnected <- struct{}{}
}

func waitFor(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func startWS(t *testing.T, h Handler) (*WebSocket, string) {
	t.Helper()
	ws, err := NewWebSocket(WebSocketConfig{Addr: "127.0.0.1:0", AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = ws.Stop(context.Background()) })
	if err := ws.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	return ws, "ws://" + ws.Addr() + "/usp"
}

func dial(t *testing.T, url string, subprotocols ...string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: subprotocols})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func TestWebSocketRequiresSubprotocol(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// No subprotocol offered: the server must not establish a session.
	c, resp, err := websocket.Dial(ctx, url+"?eid=os%3A%3A012345-AAAA", nil)
	if err == nil {
		c.CloseNow()
		t.Fatal("dial without v1.usp succeeded; R-WS.12a requires refusal")
	}
	if resp != nil && resp.StatusCode == http.StatusSwitchingProtocols {
		t.Error("server upgraded without the v1.usp subprotocol")
	}
}

func TestWebSocketEndpointFromQuery(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	// eid=os::012345-AAAA, with "::" percent-encoded per R-WS.10b/10c.
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	h.mu.Lock()
	got := h.connects[0].Endpoint()
	h.mu.Unlock()
	if got != usp.EndpointID("os::012345-AAAA") {
		t.Errorf("Endpoint() = %q, want os::012345-AAAA", got)
	}
	if h.connects[0].Kind() != KindWebSocket {
		t.Errorf("Kind() = %q, want WebSocket", h.connects[0].Kind())
	}
}

func TestWebSocketMissingEIDRejected(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"v1.usp"}})
	if err == nil {
		c.CloseNow()
		t.Fatal("dial without eid succeeded; the agent must identify itself in the handshake")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400 before upgrade", resp)
	}
}

func TestWebSocketDeliversBinaryRecord(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	if err := c.Write(context.Background(), websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h.received, "OnRecord")
	h.mu.Lock()
	in := h.records[0]
	h.mu.Unlock()
	if string(in.Record) != "\x01\x02\x03" {
		t.Errorf("Record = %v, want [1 2 3]", in.Record)
	}
	if in.Conn.Endpoint() != "os::012345-AAAA" {
		t.Errorf("Inbound.Conn.Endpoint() = %q", in.Conn.Endpoint())
	}
	if in.ReceivedAt.IsZero() {
		t.Error("ReceivedAt not set")
	}
}

func TestWebSocketRejectsTextFrame(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	_ = c.Write(context.Background(), websocket.MessageText, []byte("not a record"))
	waitFor(t, h.disconnected, "OnDisconnect after a text frame")
	// The client must observe a close status, not a silent drop.
	_, _, err := c.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusUnsupportedData {
		t.Errorf("client saw close status %v, want StatusUnsupportedData (R-WS.14)", websocket.CloseStatus(err))
	}
}

func TestWebSocketSend(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	h.mu.Lock()
	server := h.connects[0]
	h.mu.Unlock()
	if err := server.Send(context.Background(), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "hello" {
		t.Errorf("client received (%v, %q), want (Binary, hello)", typ, data)
	}
}

func TestWebSocketDisconnectOnce(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	waitFor(t, h.connected, "OnConnect")
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, h.disconnected, "OnDisconnect")
	select {
	case <-h.disconnected:
		t.Error("OnDisconnect fired twice for one connection")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWebSocketPlaintextRequiresOptIn(t *testing.T) {
	if _, err := NewWebSocket(WebSocketConfig{Addr: "127.0.0.1:0"}, slog.Default()); err == nil {
		t.Error("NewWebSocket with no TLS and no AllowPlaintext succeeded; plaintext must be opt-in")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/usp/mtp/ -run TestWebSocket -v`

Expected: compile FAIL — `undefined: NewWebSocket`, `undefined: WebSocketConfig`.

- [ ] **Step 4: Implement the contract**

Create `websocket.go` implementing the **Contract** above. Non-obvious library points, so you do not have to rediscover them: `websocket.Accept` negotiates the subprotocol from `AcceptOptions.Subprotocols` and `conn.Subprotocol()` reports the result; the library answers pings only while `Read` is in progress, so the read loop must run for the whole connection; `conn.SetReadLimit(n)` enforces `MaxRecordBytes`; `websocket.CloseStatus(err)` extracts the peer's close code from a read error. Fire `OnDisconnect` from the read loop's exit path only, guarded by a `sync.Once`, so `Close` racing a peer close cannot double-fire it. Use `http.Server` with `ReadHeaderTimeout` set, consistent with the other services.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/usp/mtp/ -v` (or without `-race` locally if CGO is unavailable; say so).

Expected: all PASS.

- [ ] **Step 6: Full backend checks, then commit**

`git diff go.mod` must show only the `coder/websocket` require (plus any `// indirect` lines `go mod tidy` derived from it).

```bash
git add go.mod go.sum internal/usp/mtp/websocket.go internal/usp/mtp/websocket_test.go
git commit -F - <<'EOF'
feat(usp/mtp): WebSocket transport on coder/websocket

Negotiates the mandatory v1.usp subprotocol and refuses a session
without it, reads the agent's endpoint id from the eid query parameter
per the USP WebSocket binding, accepts only binary frames, and keeps a
read loop alive for the connection's life so control-frame pings are
answered. Plaintext is opt-in.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 4: MQTT transport on an embedded broker

**Files:**
- Create: `backend/internal/usp/mtp/mqtt_topics.go`, `backend/internal/usp/mtp/mqtt.go`
- Test: `backend/internal/usp/mtp/mqtt_topics_test.go`, `backend/internal/usp/mtp/mqtt_test.go`
- Modify: `backend/go.mod`, `backend/go.sum` (adds `github.com/mochi-mqtt/server/v2 v2.7.9`)

**Interfaces:**
- Consumes: Task 2's types.
- Produces:
  - Pure helpers in `mqtt_topics.go`: `func ReplyToFromV311Topic(publishedTopic, controllerTopic string) (agentTopic string, ok bool)` — parses `<controllerTopic>/reply-to=<escaped>` and unescapes `%2F` to `/`; `func EscapeReplyTo(agentTopic string) string` — the inverse, `/` → `%2F`; `const ContentTypeUSP = "usp.msg"`.
  - `type MQTTConfig struct { Addr string; ControllerTopic string; TLS *tls.Config; AllowPlaintext bool }`, `func NewMQTT(cfg MQTTConfig, log *slog.Logger) (*MQTT, error)`, `*MQTT` implementing `Transport`.

**Contract:**
- `NewMQTT` builds `mqtt.New(&mqtt.Options{InlineClient: true})`, adds `auth.AllowHook` (B-3 replaces it with the allowlist), and a TCP listener via `listeners.NewTCP(listeners.Config{ID: "usp", Address: cfg.Addr, TLSConfig: cfg.TLS})`. With `TLS == nil` and `!AllowPlaintext`, return an error.
- `Start` subscribes the inline client to `cfg.ControllerTopic + "/#"` — the `/#` wildcard is required so a v3.1.1 agent's `.../reply-to=...` suffix is received.
- On each inbound PUBLISH, derive the agent's reply topic: if the client's `Properties.ProtocolVersion == 5`, from `pk.Properties.ResponseTopic`; else from `ReplyToFromV311Topic`. If neither yields a topic, log and drop — a record with no reply path cannot be answered. Derive the agent's Endpoint ID by decoding the Record's `from_id`: the transport calls `usp.DecodeRecord` **only** to read `From` (it needs the id to key the registry), then hands the raw bytes to `Handler.OnRecord` unchanged. If `DecodeRecord` fails with anything other than `ErrNoPayload`, drop and log.
- `Conn.Send` publishes to the agent's reply topic. For a v5 agent, build a `packets.Packet` PUBLISH with `Properties.ResponseTopic = cfg.ControllerTopic` and `Properties.ContentType = ContentTypeUSP`, and deliver it with `server.InjectPacket` from the inline client — plain `server.Publish` cannot carry properties. For a v3.1.1 agent, publish to `agentTopic + "/reply-to=" + EscapeReplyTo(cfg.ControllerTopic)`.
- A `Conn` for MQTT is a logical connection: it exists from the first record received from an Endpoint ID and ends when the broker reports the client disconnected. `OnConnect` fires on first record; `OnDisconnect` on broker client-disconnect for that client id.
- `Addr()` returns the listener's bound address.

**Checklist:**
| Requirement | Test |
|---|---|
| v3.1.1 reply-to parsed and unescaped | `TestReplyToFromV311Topic` |
| Escape/unescape round-trip incl. `%` and nested `/` | `TestEscapeReplyToRoundTrip` |
| Non-matching topics rejected | `TestReplyToFromV311TopicRejectsForeign` |
| Plaintext refused unless allowed | `TestMQTTPlaintextRequiresOptIn` |
| Broker starts, `Addr()` bound, `Stop` clean | `TestMQTTStartStop` |
| Real-wire v5 and v3.1.1 exchange | Task 6 (obuspa in CI) — a real MQTT client is deliberately not added as a test dependency |

- [ ] **Step 1: Add the dependency**

Run, from `backend/`: `go get github.com/mochi-mqtt/server/v2@v2.7.9 && go mod tidy`

`git diff go.mod` must show one new direct require plus indirect lines. Record it. Also record `go list -deps ./internal/usp/mtp/ | grep -vE '^acs/|^[a-z]+(/|$)' | sort -u` in your report — the modules actually linked — so the dependency footprint is on the record, not assumed.

- [ ] **Step 2: Write the failing pure tests**

Create `backend/internal/usp/mtp/mqtt_topics_test.go`:

```go
package mtp

import "testing"

func TestReplyToFromV311Topic(t *testing.T) {
	const ctrl = "/usp/controller"
	cases := map[string]string{
		"/usp/controller/reply-to=%2Fusp%2Fagent":           "/usp/agent",
		"/usp/controller/reply-to=%2Fusp%2Fagent%2Fcpe-1":   "/usp/agent/cpe-1",
		"/usp/controller/reply-to=agent-no-slashes":         "agent-no-slashes",
	}
	for in, want := range cases {
		got, ok := ReplyToFromV311Topic(in, ctrl)
		if !ok || got != want {
			t.Errorf("ReplyToFromV311Topic(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
}

func TestReplyToFromV311TopicRejectsForeign(t *testing.T) {
	const ctrl = "/usp/controller"
	for _, in := range []string{
		"/usp/controller",                      // no reply-to suffix at all
		"/usp/controller/other=thing",          // wrong key
		"/usp/other/reply-to=%2Fusp%2Fagent",   // wrong controller topic
		"/usp/controller/reply-to=",            // empty agent topic
	} {
		if got, ok := ReplyToFromV311Topic(in, ctrl); ok {
			t.Errorf("ReplyToFromV311Topic(%q) = (%q, true), want ok=false", in, got)
		}
	}
}

func TestEscapeReplyToRoundTrip(t *testing.T) {
	for _, topic := range []string{"/usp/agent", "/usp/agent/cpe-1", "plain", "with%percent", "/a/b/c/d"} {
		esc := EscapeReplyTo(topic)
		if esc != topic && containsRune(esc, '/') {
			t.Errorf("EscapeReplyTo(%q) = %q still contains '/'", topic, esc)
		}
		got, ok := ReplyToFromV311Topic("/c/reply-to="+esc, "/c")
		if !ok || got != topic {
			t.Errorf("round trip of %q via %q gave (%q, %v)", topic, esc, got, ok)
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
```

And `backend/internal/usp/mtp/mqtt_test.go`:

```go
package mtp

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestMQTTPlaintextRequiresOptIn(t *testing.T) {
	if _, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller"}, slog.Default()); err == nil {
		t.Error("NewMQTT with no TLS and no AllowPlaintext succeeded; plaintext must be opt-in")
	}
}

func TestMQTTStartStop(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	if m.Kind() != KindMQTT {
		t.Errorf("Kind() = %q, want MQTT", m.Kind())
	}
	// The broker must be listening: a raw TCP connect succeeds.
	c, err := net.DialTimeout("tcp", m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("broker not listening on %s: %v", m.Addr(), err)
	}
	c.Close()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/usp/mtp/ -run 'TestReplyTo|TestEscape|TestMQTT' -v`

Expected: compile FAIL on the undefined helpers and `NewMQTT`.

- [ ] **Step 4: Implement the contract**

`mqtt_topics.go` first — pure string handling, no broker. Then `mqtt.go` per the **Contract**. The library points that are not obvious from its README: `server.Subscribe(filter, subscriptionID, fn)` registers the inline client; `fn` receives `(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet)` and `cl.Properties.ProtocolVersion` is `5` for MQTT 5 and `4` for 3.1.1; `pk.Properties.ResponseTopic` and `.ContentType` are the v5 property fields; `server.InjectPacket(cl, pk)` is the only path that carries v5 properties on an outbound PUBLISH — construct `pk` with `FixedHeader.Type = packets.Publish`, `TopicName`, `Payload`, and `Properties`. Broker disconnect notifications come through a hook: implement a small type embedding `mqtt.HookBase` whose `OnDisconnect(cl *mqtt.Client, err error, expire bool)` maps the client back to the logical `Conn` and fires `Handler.OnDisconnect`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/usp/mtp/ -v`

Expected: all PASS.

- [ ] **Step 6: Full backend checks, then commit**

```bash
git add go.mod go.sum internal/usp/mtp/mqtt_topics.go internal/usp/mtp/mqtt_topics_test.go internal/usp/mtp/mqtt.go internal/usp/mtp/mqtt_test.go
git commit -F - <<'EOF'
feat(usp/mtp): embedded MQTT transport on mochi-mqtt

An embedded broker rather than an external one, so no new
infrastructure and no new administrator ask. Both reply-to conventions
the reference agent uses are supported: the MQTT 5 Response Topic
property, and the 3.1.1 "/reply-to=<%2F-escaped topic>" suffix.
Outbound v5 publishes go through InjectPacket, the only path that
carries properties. Plaintext is opt-in.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 5: `cmd/uspc` — the controller service

**Files:**
- Create: `backend/cmd/uspc/main.go`, `backend/cmd/uspc/probe.go`, `backend/cmd/uspc/boundary_test.go`, `backend/cmd/uspc/probe_test.go`, `backend/Dockerfile.uspc`
- Modify: `infra/docker-compose.yml` (add `uspc` to the `containerized` profile), `README.md` (component table row), `deployment-testing-onboarding-guide.md` §7 (new variables)

**Interfaces:**
- Consumes: everything from Tasks 1–4; `usp.EncodeGet`, `usp.EncodeRecord`, `usp.DecodeRecord`, `usp.DecodeMsg`, `usp.NewMsgID`, `usp.ErrorFromMsg`; `internal/config`, `internal/observability`.
- Produces: the `uspc` binary and its configuration surface.

**Configuration (all read at startup, fail-closed):**
| Variable | Rule |
|---|---|
| `ACS_USP_CONTROLLER_ID` | required; ≥ 8 bytes; matches `^[A-Za-z0-9._-]+$`; not a placeholder. Controller EndpointID is `self::` + this. |
| `ACS_USP_WS_ADDR` | default `:9877` |
| `ACS_USP_WS_PATH` | default `/usp` |
| `ACS_USP_MQTT_ADDR` | default `:1883` |
| `ACS_USP_MQTT_CONTROLLER_TOPIC` | default `/usp/controller` |
| `ACS_USP_TLS_CERT`, `ACS_USP_TLS_KEY` | both or neither; if set, both transports use TLS |
| `ACS_USP_ALLOW_PLAINTEXT` | `true` permits running with no TLS; default false |
| `ACS_USP_HTTP_ADDR` | default `:8092` — `/healthz`, `/readyz`, `/metrics` |

**Contract:**
- Mirror `cmd/bssadapter/main.go` for structure: `slog` logger, `config.Validate` / `config.LogSummary`, `signal.NotifyContext`, an `http.Server` with `ReadHeaderTimeout`, graceful `Shutdown`, and `observability.NewMetrics("uspc")`.
- Since there is no database in this plan, `/readyz` returns 200 once both transports have started; `/healthz` always 200 while the process runs.
- Metrics: gauge `acs_usp_connections{mtp}`, counter `acs_usp_records_total{mtp,direction,result}` with `direction` in `in`/`out` and `result` in `ok`/`decode_error`/`no_payload`/`unsupported`.
- The `Handler` implementation: `OnConnect` → `registry.Add`, close any replaced conn with reason `"replaced by new connection"`, then run the probe. `OnRecord` → `usp.DecodeRecord(in.Record, controllerID)`; on `ErrNoPayload` log the record type (and disconnect reason if any) at info; on `ErrSessionContextUnsupported` log at warn; on any other error log at warn and — for WebSocket only — `Close` the conn with reason `"undecodable record"` (R-WS.16); on success `usp.DecodeMsg` and hand to the probe's response matcher. `OnDisconnect` → `registry.Remove`.
- **The probe** (`probe.go`): on connect, send `Get(["Device.DeviceInfo."], 1)` wrapped in a Record from the controller to the agent, remembering the `msg_id`. When a `GetResp` with that `msg_id` arrives, log at info: endpoint id, MTP, and every `param_path`/value in the response. When an `Error` body arrives for that `msg_id`, log the `USPError`. Probe state is a mutex-guarded map `msgID → endpoint`, entries removed on match; unmatched entries are dropped when the conn disconnects.
- At startup, log at **warn**: `"agent allowlisting is not implemented in this build; do not expose uspc to an untrusted network"`.
- `Dockerfile.uspc` follows `Dockerfile.bssadapter` exactly, exposing 9877, 1883 and 8092.

**Checklist:**
| Requirement | Test |
|---|---|
| Missing / short / placeholder `ACS_USP_CONTROLLER_ID` is fatal | `TestControllerIDFailsClosed` |
| TLS cert without key (or vice versa) is fatal | `TestTLSPairRequired` |
| No TLS and no plaintext flag is fatal | `TestPlaintextRequiresOptIn` |
| Probe matches a `GetResp` to its `msg_id` and extracts values | `TestProbeMatchesResponse` |
| Probe reports an `Error` body for its `msg_id` | `TestProbeReportsError` |
| Unmatched `msg_id` is ignored, not a crash | `TestProbeIgnoresUnknownMsgID` |
| `cmd/uspc` imports no domain package | `TestUSPCImportsNoDomainPackages` |
| Service boots with CI-style env and serves `/readyz` 200 | Task 6's CI job |

- [ ] **Step 1: Write the failing tests**

Factor config parsing into `func loadConfig(getenv func(string) string, log *slog.Logger) (config, error)` so it is testable without touching the process environment; and the probe into a `probe` type with `func (p *probe) start(ctx context.Context, c mtp.Conn) error` and `func (p *probe) handle(from usp.EndpointID, msg *uspproto.Msg) (matched bool)`.

Create `backend/cmd/uspc/probe_test.go`:

```go
package main

import (
	"context"
	"log/slog"
	"testing"

	"acs/internal/usp"
	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// captureConn records what the probe sends so the test can read the
// msg_id back out of the Get and answer it.
type captureConn struct {
	id   usp.EndpointID
	sent [][]byte
}

func (c *captureConn) Endpoint() usp.EndpointID          { return c.id }
func (c *captureConn) Kind() mtpKindForTest              { return "WebSocket" }
func (c *captureConn) RemoteAddr() string                { return "test" }
func (c *captureConn) Close(string) error                { return nil }
func (c *captureConn) Send(_ context.Context, r []byte) error { c.sent = append(c.sent, r); return nil }

func sentMsgID(t *testing.T, controller, agent usp.EndpointID, wire []byte) string {
	t.Helper()
	rec, err := usp.DecodeRecord(wire, agent) // the agent is the recipient
	if err != nil {
		t.Fatalf("decode probe record: %v", err)
	}
	if rec.From != controller {
		t.Fatalf("probe record From = %q, want %q", rec.From, controller)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_GET {
		t.Fatalf("probe sent %v, want GET", msg.GetHeader().GetMsgType())
	}
	if paths := msg.GetBody().GetRequest().GetGet().GetParamPaths(); len(paths) != 1 || paths[0] != "Device.DeviceInfo." {
		t.Fatalf("probe Get paths = %v, want [Device.DeviceInfo.]", paths)
	}
	return msg.GetHeader().GetMsgId()
}

func getResp(msgID string, params map[string]string) *uspproto.Msg {
	results := []*uspproto.GetResp_ResolvedPathResult{{ResolvedPath: "Device.DeviceInfo.", ResultParams: params}}
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{
				ReqPathResults: []*uspproto.GetResp_RequestedPathResult{{RequestedPath: "Device.DeviceInfo.", ResolvedPathResults: results}},
			}},
		}}},
	}
}

const (
	ctrl  = usp.EndpointID("self::acs-test")
	agent = usp.EndpointID("os::012345-AAAA")
)

func TestProbeMatchesResponse(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	c := &captureConn{id: agent}
	if err := p.start(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("probe sent %d records, want 1", len(c.sent))
	}
	id := sentMsgID(t, ctrl, agent, c.sent[0])
	if !p.handle(agent, getResp(id, map[string]string{"SoftwareVersion": "11.0.7"})) {
		t.Error("handle returned false for the probe's own msg_id")
	}
	if p.handle(agent, getResp(id, nil)) {
		t.Error("handle matched the same msg_id twice; entries must be consumed")
	}
}

func TestProbeReportsError(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	c := &captureConn{id: agent}
	_ = p.start(context.Background(), c)
	id := sentMsgID(t, ctrl, agent, c.sent[0])
	errMsg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: id, MsgType: uspproto.Header_ERROR},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{ErrCode: 7006, ErrMsg: "permission denied"}}},
	}
	if !p.handle(agent, errMsg) {
		t.Error("handle returned false for an Error body carrying the probe's msg_id")
	}
}

func TestProbeIgnoresUnknownMsgID(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	if p.handle(agent, getResp("never-sent", nil)) {
		t.Error("handle matched a msg_id the probe never sent")
	}
	// And a nil / bodiless message must not panic.
	_ = p.handle(agent, &uspproto.Msg{Header: &uspproto.Header{MsgId: "x"}})
	_ = proto.Size // keep the import honest if the file above does not otherwise use it
}
```

`captureConn` must satisfy `mtp.Conn`; define `type mtpKindForTest = mtp.Kind` and import `acs/internal/usp/mtp` so the alias resolves — the point is only to keep the test's fake small.

Then `backend/cmd/uspc/boundary_test.go` — an external-package test asserting `build.Import("acs/cmd/uspc", "", 0).Imports` contains nothing with prefix `acs/internal/devices`, `acs/internal/jobs`, `acs/internal/store`, using the same `imported == root || strings.HasPrefix(imported, root+"/")` predicate as B-1's guard.

And a config test file `backend/cmd/uspc/config_test.go` with `TestControllerIDFailsClosed` (empty, 7 bytes, `change-me`, and a value with a space each return an error), `TestTLSPairRequired` (cert without key errors), `TestPlaintextRequiresOptIn` (no cert, no key, flag unset → error; flag `true` → ok), all driven through `loadConfig` with a map-backed `getenv`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -v`

Expected: compile FAIL — the package does not exist.

- [ ] **Step 3: Implement the contract**

Write `main.go` (wiring), `probe.go`, and the config loader per the **Contract** and **Configuration** tables. Keep `main.go` to wiring: if a function in it exceeds ~40 lines, it is doing work that belongs in a helper. Write `Dockerfile.uspc`, add the compose service, the README row, and the guide's variable table entries.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/ -v` — all PASS.

- [ ] **Step 5: Boot it locally against the plan's CI environment**

From `backend/`:

```bash
ACS_USP_CONTROLLER_ID=ci-controller ACS_USP_ALLOW_PLAINTEXT=true \
ACS_USP_WS_ADDR=127.0.0.1:19877 ACS_USP_MQTT_ADDR=127.0.0.1:11883 ACS_USP_HTTP_ADDR=127.0.0.1:18092 \
go run ./cmd/uspc &
sleep 3
curl -s -w " [HTTP %{http_code}]\n" http://127.0.0.1:18092/readyz
curl -s http://127.0.0.1:18092/metrics | grep -c '^acs_usp_'
kill %1
```

Expected: `ready [HTTP 200]` and a non-zero metric count. Record the output.

- [ ] **Step 6: Non-textual property check**

`Dockerfile.uspc` needs no executable bit. Confirm no file added in this task is a script: `git ls-files -s cmd/uspc Dockerfile.uspc` shows `100644` for everything. Record it.

- [ ] **Step 7: Full backend checks, then commit**

`git diff go.mod` must be empty for this task.

```bash
git add cmd/uspc Dockerfile.uspc ../infra/docker-compose.yml ../README.md ../deployment-testing-onboarding-guide.md
git commit -F - <<'EOF'
feat(uspc): the USP controller service

Wiring only: fail-closed config, both transports, the connection
registry, health and metrics, and graceful shutdown, mirroring the
other services. On every new connection it sends a Get for
Device.DeviceInfo. and logs the answer -- the interop evidence the CI
job asserts on, and the probe B-3 replaces with real dispatch.

The controller endpoint id is self:: plus a configured identifier, not
a hostname: the agent's controller table stores it verbatim, so an id
that drifted across restarts would silently orphan every agent.

Agent allowlisting is B-3. Until then this service logs at startup that
it must not face an untrusted network.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 6: obuspa interop in CI — the acceptance gate

**Why:** Spec §9: "protocol code that has never met a real agent is an intention, not a capability." This task is the deliverable of B-2. It runs the Broadband Forum reference agent against `cmd/uspc` on each MTP and asserts the controller logged a `GetResp` for `Device.DeviceInfo.`.

**Files:**
- Create: `ci/usp/obuspa-websocket.txt`, `ci/usp/obuspa-mqtt.txt` (factory-reset configs), `ci/usp/assert-getresp.sh`, `ci/usp/README.md`
- Modify: `.github/workflows/ci.yml` (new `usp-interop` job)

**Interfaces:**
- Consumes: the `uspc` binary and its env from Task 5; obuspa's `ci/Dockerfile` and `ci/configs/*.txt` from `github.com/BroadbandForum/obuspa` — pin to a commit, not a branch.

**Contract:**
- The job builds obuspa from its `ci/Dockerfile` at a **pinned commit** (record the SHA in the workflow with a comment naming the release, e.g. the v11.0.7 tag's commit). Build with `--disable-coap` — CoAP is out of scope and unmaintained there.
- `uspc` is built with `go build` and run in the background with the CI env from Task 5 plus `ACS_USP_WS_ADDR=0.0.0.0:9877`, `ACS_USP_MQTT_ADDR=0.0.0.0:1883` so the container can reach it via `host.docker.internal` / `--network host`.
- **WebSocket run:** a factory-reset file derived from obuspa's `websockets_factory_reset_example.txt`, with the agent's ws-client pointed at `host:9877` path `/usp`, `EnableEncryption "false"`, `Device.LocalAgent.EndpointID "proto::ci-agent"`, and `Device.LocalAgent.Controller.1.EndpointID "self::ci-controller"` — the controller id **must** match `ACS_USP_CONTROLLER_ID`, because obuspa refuses records from an unprovisioned controller (spec §3.4).
- **MQTT run:** derived from obuspa's `ci/configs/MQTT.txt`: broker address the host, port 1883, `ProtocolVersion "5.0"`, `Device.LocalAgent.MTP.1.MQTT.ResponseTopicConfigured "/usp/agent"`, controller topic `/usp/controller`, same endpoint ids. A second MQTT run with `ProtocolVersion "3.1.1"` covers the reply-to suffix path.
- obuspa runs with `-p` (protobuf trace) so its log is evidence too, and a fresh `-f /tmp/usp.db` each run — the factory-reset file is ignored if a database exists.
- `assert-getresp.sh` polls the `uspc` log for up to 60 s for a line containing the agent's endpoint id, the MTP name, and `Device.DeviceInfo.SoftwareVersion`; on timeout it prints both logs and exits 1.
- Each of the three runs (WS, MQTT v5, MQTT v3.1.1) is its own step, so a failure names the transport.
- On success, append a row to `docs/COMPATIBILITY.md`'s protocol table for USP: `obuspa <version>, WebSocket + MQTT 5 + MQTT 3.1.1, Get/GetResp — validated in CI` — the first real-agent row that document has ever had.

**Checklist:**
| Requirement | Evidence |
|---|---|
| obuspa connects over WebSocket and answers `Get` | CI step `usp-interop: websocket` green, log line asserted |
| obuspa connects over MQTT 5 and answers `Get` | CI step `usp-interop: mqtt-v5` |
| obuspa connects over MQTT 3.1.1 (reply-to suffix) and answers `Get` | CI step `usp-interop: mqtt-v311` |
| Version pinned, not floating | the workflow references a commit SHA |
| Scripts executable in the committed tree | Step 4 below |
| Result recorded where the project records compatibility | `docs/COMPATIBILITY.md` row |

- [ ] **Step 1: Write the assertion script and the two factory-reset files**

`ci/usp/assert-getresp.sh`:

```bash
#!/usr/bin/env bash
# Poll the uspc log for the probe's GetResp from a given agent over a
# given MTP. Prints both logs and fails on timeout, so a red step shows
# what each side said rather than just "timed out".
set -euo pipefail
log="$1"; agent="$2"; mtp="$3"; obuspa_log="${4:-}"
for _ in $(seq 1 60); do
  if grep -q "GetResp" "$log" && grep -q "$agent" "$log" && grep -q "$mtp" "$log" && grep -q "Device.DeviceInfo.SoftwareVersion" "$log"; then
    echo "OK: $agent answered Get over $mtp"; grep "GetResp" "$log" | tail -3; exit 0
  fi
  sleep 1
done
echo "FAIL: no GetResp from $agent over $mtp within 60s"
echo "--- uspc log ---"; cat "$log"
[ -n "$obuspa_log" ] && { echo "--- obuspa log ---"; cat "$obuspa_log"; }
exit 1
```

Write the two factory-reset files by copying obuspa's examples at the pinned commit and changing only: the controller endpoint id, the agent endpoint id, the host/port, and `EnableEncryption`/`TransportProtocol` to plaintext. Keep a comment at the top of each naming the upstream file and commit it was derived from.

- [ ] **Step 2: Add the CI job**

Add a `usp-interop` job to `.github/workflows/ci.yml` that: checks out; sets up Go from `backend/go.mod`; builds `uspc`; clones obuspa at the pinned SHA and builds its `ci/Dockerfile` image; then three steps, each starting `uspc` fresh with the Task 5 env (and `ACS_USP_ALLOW_PLAINTEXT=true`), running the obuspa container with the matching factory-reset file and `--network host`, and calling `assert-getresp.sh`. Make the job `needs: [go]` so it does not run on a build that already fails.

- [ ] **Step 3: Validate the workflow**

From the repo root: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"`.

- [ ] **Step 4: Non-textual property — executable bit**

```bash
git add ci/usp/assert-getresp.sh
git update-index --chmod=+x ci/usp/assert-getresp.sh
git ls-files -s ci/usp/assert-getresp.sh   # must print 100755
```

A plain `chmod +x` does not reach the index on Windows; this step exists because B-1's Task 1 shipped a script as `100644` and the CI step invoking it would have failed with "permission denied".

- [ ] **Step 5: Run the interop locally if Docker is available**

If Docker is usable on this machine, execute the WebSocket run exactly as the CI step does, from the repo root, and record the assertion output. If Docker is not usable, say so plainly in the report; the CI job is then the first real execution and its result must be watched.

- [ ] **Step 6: Record the result and commit**

Add the `docs/COMPATIBILITY.md` row. Then:

```bash
git add ci/usp .github/workflows/ci.yml docs/COMPATIBILITY.md
git commit -F - <<'EOF'
ci: prove USP interop against the reference agent on both transports

Runs obuspa, pinned by commit, against cmd/uspc over WebSocket, MQTT 5
and MQTT 3.1.1, and asserts the controller logged a GetResp for
Device.DeviceInfo. from the agent on each. Protocol code that has never
met a real agent is an intention, not a capability; this is the
capability.

Records the result in COMPATIBILITY.md -- the first real-agent row that
document has ever carried.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.**

| Spec section | Task |
|---|---|
| §4.1 `internal/usp/mtp` transport abstraction, both implementations | 2, 3, 4 |
| §4.1 `cmd/uspc` as wiring only; dependency rule extended to `cmd/uspc` | 5 (boundary test) |
| §6.1 connection registry, one conn per endpoint, seam for HA | 2 |
| §3.4 `v1.usp`, binary frames, eid in handshake | 3 |
| §3.4 MQTT v5 Response Topic + `usp.msg`; v3.1.1 reply-to suffix | 4 |
| §3.4 controller EndpointID pre-provisioned in agent; `to_id` exact | 5 (config), 6 (factory-reset files) |
| §8 TLS default, plaintext only for CI | 3, 4, 5 |
| §8 agent allowlist | **deliberately deferred to B-3** — stated in *Decisions settled here*; Task 5 logs the exposure warning |
| §9 obuspa as acceptance gate, both MTPs, results in `COMPATIBILITY.md` | 6 |
| §9 `Record.version` 1.3 expected | unchanged from B-1 |
| Open question 2 — controller EndpointID scheme | *Decisions settled here* |
| B-1 deferral: `DecodeRecord` record type | 1 |
| §10 CoAP, STOMP, session context out of scope | 1 refuses session context; 6 builds obuspa `--disable-coap`; STOMP not implemented |

Deliberately not here: `OnBoardRequest` handling, `usp_agents`, dispatch, subscriptions, the `from_id` allowlist — all B-3. `DecodeRecord` still accepts an empty `from_id` (B-1 minor); B-3's allowlist rejects it there.

**2. Placeholder scan.** No `TBD`/`TODO`/"implement later"/"similar to Task N". Every test is literal code. Implementation steps state a contract with the specific library calls named; that is the deliberate shape of this plan, not a placeholder.

**3. Type consistency.** `Conn`, `Handler`, `Inbound`, `Transport`, `Kind`, `Registry` are defined in Task 2 and consumed by 3, 4 and 5 under those names. `WebSocketConfig`/`NewWebSocket` (3) and `MQTTConfig`/`NewMQTT` (4) are consumed by 5. `RecordType`, `ErrSessionContextUnsupported` and the new `DecodedRecord` fields (1) are consumed by 5's `OnRecord`. `newProbe`, `probe.start`, `probe.handle`, `loadConfig` are defined and tested within 5. `ReplyToFromV311Topic`/`EscapeReplyTo` (4) are used only inside 4. The `recordingHandler` and `waitFor` helpers are defined in 3's test file and reused by 4's `TestMQTTStartStop` in the same package.

**4. Checklist ↔ test cross-check (the B-1 lesson).** Every row in every task's checklist names a test that appears in that task's Step 1 code, or names the CI step that is its evidence. Two rows point at Task 6 rather than a unit test — the real-wire MQTT exchange — and say so explicitly rather than pretending a unit test exists.

**Ordering.** 1 → 2 → 3 → 4 → 5 → 6 strictly. 3 and 4 could run in either order but both need 2, and 5 needs both.
