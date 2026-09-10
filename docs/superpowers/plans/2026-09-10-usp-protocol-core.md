# USP Protocol Core Implementation Plan (B-1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the transport-free USP protocol core — protobuf bindings, Record envelope, Endpoint IDs, message encoding/decoding and error mapping — so later plans can put a transport under it and a domain above it.

**Architecture:** `internal/usp` speaks protocol only and imports nothing from `devices`, `jobs` or `store` (spec §4.1). Generated protobuf lives in a leaf package `internal/usp/uspproto`; hand-written codec, identity and error mapping sit in `internal/usp`. Everything here is pure and unit-testable: no network, no database, no goroutines.

**Tech Stack:** Go 1.26, `google.golang.org/protobuf` (already in the module graph as an indirect dependency; this plan makes it direct). Generated `.pb.go` files are committed, with a CI check that regenerating produces no diff. `protoc` and `protoc-gen-go` are needed only to change the schema, not to build or test.

**Spec:** [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) — this plan implements §3 (verified protocol facts), §4.1's dependency rule, §5.3's identity ordering (the parsing half), and §6.4 (error mapping). Read both.

## Programme context

This is plan **B-1** of three for sub-project B:

| Plan | Deliverable | Depends on |
|---|---|---|
| **B-1** (this) | Protocol core: codec, Endpoint IDs, error mapping. No transport, no DB. | — |
| B-2 | MTP abstraction, WebSocket (`coder/websocket`), MQTT (embedded `mochi-mqtt`), connection registry, `cmd/uspc`, obuspa interop in CI. | B-1 |
| B-3 | `OnBoardRequest` identity reconciliation, job→USP dispatch, subscriptions and Notify routing. | B-2, sub-project A |

## Global Constraints

- Go module is `acs`; module Go directive is `go 1.26.6`. Do not raise it.
- **`internal/usp` and `internal/usp/uspproto` must not import `acs/internal/devices`, `acs/internal/jobs`, `acs/internal/store`, or any `acs/cmd/...` package.** This is spec §4.1's dependency rule and Task 6 enforces it with a test.
- The only new direct dependency permitted is `google.golang.org/protobuf`, which is already in the module graph as indirect. No other third-party module may be added in this plan.
- Vendored `.proto` files are **upstream bytes, unmodified** — BSD-3-Clause, copyright Broadband Forum and ARRIS. Never edit them; not even to add `option go_package`. Codegen supplies the Go package via `M` mapping flags instead.
- Generated `.pb.go` files are committed. Never hand-edit them.
- The controller must accept `Record.version == "1.3"` and must not require `"1.4"` (spec §3.4 — obuspa emits 1.3 despite "USP 1.4" release notes).
- Non-plaintext `payload_security` is rejected (spec §10: E2E/encrypted payloads are out of scope, and obuspa rejects them too).
- Before every commit, from `backend/`: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.
- Commit message style: `type(scope): summary`, imperative mood. End each commit message with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/usp/proto/usp-record-1-3.proto` | Vendored upstream Record schema (unmodified). |
| `backend/internal/usp/proto/usp-msg-1-3.proto` | Vendored upstream Message schema (unmodified). |
| `backend/internal/usp/proto/LICENSE.txt` | Upstream BSD-3-Clause licence text. |
| `backend/internal/usp/proto/generate.sh` | The exact codegen command, so regeneration is reproducible. |
| `backend/internal/usp/uspproto/*.pb.go` | Generated bindings. Committed, never hand-edited. |
| `backend/internal/usp/endpoint.go` | Endpoint ID formatting, parsing and identity extraction. |
| `backend/internal/usp/record.go` | Record envelope encode/decode and validation. |
| `backend/internal/usp/message.go` | Msg envelope, request encoding, response/error decoding. |
| `backend/internal/usp/errors.go` | USP 7xxx error codes as typed Go errors. |
| `backend/internal/usp/boundary_test.go` | Enforces the §4.1 import rule. |

---

### Task 1: Vendor the schemas and generate the bindings

**Files:**
- Create: `backend/internal/usp/proto/usp-record-1-3.proto`, `backend/internal/usp/proto/usp-msg-1-3.proto`, `backend/internal/usp/proto/LICENSE.txt`, `backend/internal/usp/proto/generate.sh`
- Create: `backend/internal/usp/uspproto/` (generated `.pb.go` files)
- Modify: `backend/go.mod` (promote `google.golang.org/protobuf` from indirect to direct)
- Modify: `.github/workflows/ci.yml` (add the regeneration-diff check)

**Interfaces:**
- Consumes: nothing.
- Produces: Go package `acs/internal/usp/uspproto` containing, from proto package `usp_record`: `Record` (fields `Version`, `ToId`, `FromId`, `PayloadSecurity`, `MacSignature`, `SenderCert`, and a `RecordType` oneof with `Record_NoSessionContext`, `Record_SessionContext`, `Record_WebsocketConnect`, `Record_MqttConnect`, `Record_StompConnect`, `Record_Disconnect`, `Record_UdsConnect`), `Record_PLAINTEXT` and `Record_TLS12` enum values, `NoSessionContextRecord`, `SessionContextRecord`, `WebSocketConnectRecord`, `MQTTConnectRecord`, `STOMPConnectRecord`, `UDSConnectRecord`, `DisconnectRecord`; and from proto package `usp`: `Msg`, `Header` (with `Header_MsgType` enum values `Header_GET`, `Header_GET_RESP`, `Header_SET`, `Header_SET_RESP`, `Header_ADD`, `Header_ADD_RESP`, `Header_DELETE`, `Header_DELETE_RESP`, `Header_OPERATE`, `Header_OPERATE_RESP`, `Header_NOTIFY`, `Header_NOTIFY_RESP`, `Header_GET_SUPPORTED_DM`, `Header_GET_SUPPORTED_DM_RESP`, `Header_GET_INSTANCES`, `Header_GET_INSTANCES_RESP`, `Header_GET_SUPPORTED_PROTO`, `Header_GET_SUPPORTED_PROTO_RESP`, `Header_ERROR`), `Body`, `Request`, `Response`, `Error`, `Get`, `GetResp`, `Set`, `SetResp`, `Add`, `AddResp`, `Delete`, `DeleteResp`, `Operate`, `OperateResp`, `GetSupportedDM`, `GetSupportedDMResp`, `GetInstances`, `GetInstancesResp`, `GetSupportedProtocol`, `GetSupportedProtocolResp`, `Notify`, `NotifyResp`.

**Note on the vendored bytes.** The upstream files are in the Broadband Forum `usp` repository under `specification/`. Retrieve them from `https://codeload.github.com/BroadbandForum/usp/tar.gz/refs/heads/master` (a plain `raw.githubusercontent.com` fetch was observed returning 503; codeload works). Copy `specification/usp-record-1-3.proto`, `specification/usp-msg-1-3.proto` and `LICENSE.txt` **byte-for-byte**. Neither proto declares `option go_package`, which is why Step 3's command carries `M` flags.

- [ ] **Step 1: Vendor the three upstream files**

```bash
cd backend/internal/usp
mkdir -p proto uspproto
cd /tmp
curl -sL --max-time 180 -o usp-spec.tar.gz \
  "https://codeload.github.com/BroadbandForum/usp/tar.gz/refs/heads/master"
mkdir -p uspspec && tar -xzf usp-spec.tar.gz -C uspspec
cd -
cp /tmp/uspspec/usp-master/specification/usp-record-1-3.proto proto/
cp /tmp/uspspec/usp-master/specification/usp-msg-1-3.proto proto/
cp /tmp/uspspec/usp-master/LICENSE.txt proto/
```

Verify the two protos are the expected schemas before continuing:

```bash
grep -c "^message Record {" proto/usp-record-1-3.proto   # expect 1
grep -c "^message Msg {"    proto/usp-msg-1-3.proto      # expect 1
grep -c "go_package"        proto/*.proto                # expect 0
head -1 proto/LICENSE.txt                                # expect a Broadband Forum copyright line
```

If any count differs, stop and report — the upstream layout has changed and the rest of this task rests on these files.

- [ ] **Step 2: Write the reproducible codegen script**

Create `backend/internal/usp/proto/generate.sh`:

```bash
#!/usr/bin/env bash
# Regenerate the USP protobuf bindings.
#
# The vendored .proto files are upstream bytes and carry no
# `option go_package`, so the Go import path is supplied here with -M
# flags rather than by editing files we do not own. Both proto packages
# (usp_record and usp) map to the single Go package uspproto: they are
# always used together and share no top-level type names.
#
# Requires protoc and protoc-gen-go, which are NOT needed to build or
# test this repository -- only to change the schema. Install with:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
# and a protoc from https://github.com/protocolbuffers/protobuf/releases
#
# CI runs this and fails if the working tree changes, so the committed
# .pb.go files can never drift from the .proto they came from.
set -euo pipefail
cd "$(dirname "$0")"

protoc \
  --proto_path=. \
  --go_out=../uspproto \
  --go_opt=paths=source_relative \
  --go_opt=Musp-record-1-3.proto=acs/internal/usp/uspproto \
  --go_opt=Musp-msg-1-3.proto=acs/internal/usp/uspproto \
  usp-record-1-3.proto usp-msg-1-3.proto

echo "generated:"
ls -1 ../uspproto/*.pb.go
```

Make it executable: `chmod +x backend/internal/usp/proto/generate.sh`

- [ ] **Step 3: Install the toolchain and generate**

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
```

`protoc` itself is a C++ binary and is not installed on this machine. Install it from the protobuf releases page for your platform and put it on `PATH`. Then:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
cd backend/internal/usp/proto && ./generate.sh
```

Expected: two files written, `../uspproto/usp-record-1-3.pb.go` and `../uspproto/usp-msg-1-3.pb.go`.

**If `protoc` cannot be installed in this environment, stop and report BLOCKED.** Do not hand-write the bindings — they are ~4,000 lines of generated reflection scaffolding and a hand-written substitute would be both wrong and unmaintainable.

- [ ] **Step 4: Promote the protobuf dependency and confirm it compiles**

```bash
cd backend
go mod tidy
go build ./...
```

Expected: `go build` succeeds. `go.mod` now lists `google.golang.org/protobuf` in the **direct** require block (the `// indirect` comment is gone). No other module is added — check with `git diff go.mod`; if any other line changed, stop and report.

- [ ] **Step 5: Write a test that the generated types round-trip**

Create `backend/internal/usp/uspproto/roundtrip_test.go`:

```go
package uspproto

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestRecordRoundTrip is a smoke test that the generated bindings are
// wired up: a Record with a NoSessionContext payload survives
// marshal/unmarshal with its oneof intact. It is deliberately shallow --
// the protobuf runtime is not ours to test. What it catches is a codegen
// or M-flag mistake that produces types which compile but do not encode.
func TestRecordRoundTrip(t *testing.T) {
	in := &Record{
		Version:         "1.3",
		ToId:            "os::012345-0800270B57FF",
		FromId:          "self::acs-controller",
		PayloadSecurity: Record_PLAINTEXT,
		RecordType: &Record_NoSessionContext{
			NoSessionContext: &NoSessionContextRecord{Payload: []byte("hello")},
		},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Record
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Version != "1.3" || out.ToId != in.ToId || out.FromId != in.FromId {
		t.Errorf("envelope fields lost: %+v", &out)
	}
	if out.PayloadSecurity != Record_PLAINTEXT {
		t.Errorf("PayloadSecurity = %v, want PLAINTEXT", out.PayloadSecurity)
	}
	nsc, ok := out.GetRecordType().(*Record_NoSessionContext)
	if !ok {
		t.Fatalf("record_type oneof = %T, want *Record_NoSessionContext", out.GetRecordType())
	}
	if string(nsc.NoSessionContext.GetPayload()) != "hello" {
		t.Errorf("payload = %q, want %q", nsc.NoSessionContext.GetPayload(), "hello")
	}
}

// TestMsgRoundTrip covers the second proto package, proving both files
// landed in one Go package with their oneofs usable together.
func TestMsgRoundTrip(t *testing.T) {
	in := &Msg{
		Header: &Header{MsgId: "m-1", MsgType: Header_GET},
		Body: &Body{MsgBody: &Body_Request{Request: &Request{
			ReqType: &Request_Get{Get: &Get{ParamPaths: []string{"Device.DeviceInfo."}, MaxDepth: 1}},
		}}},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Msg
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetHeader().GetMsgId() != "m-1" || out.GetHeader().GetMsgType() != Header_GET {
		t.Errorf("header lost: %+v", out.GetHeader())
	}
	got := out.GetBody().GetRequest().GetGet().GetParamPaths()
	if len(got) != 1 || got[0] != "Device.DeviceInfo." {
		t.Errorf("param_paths = %v, want [Device.DeviceInfo.]", got)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/usp/... -v`

Expected: both PASS. If a field name differs from the test (e.g. `MsgId` vs `MsgID`), the generated name wins — protoc-gen-go's casing is not ours to choose; fix the test to match the generated code and note it in your report.

- [ ] **Step 7: Add the CI regeneration-diff check**

In `.github/workflows/ci.yml`, inside the existing `go` job (the one named `backend (fmt, vet, test -race, govulncheck)`), add a step after the existing `gofmt` step:

```yaml
      - name: USP protobuf bindings match their .proto
        run: |
          PROTOC_VERSION=29.3
          curl -sSL -o /tmp/protoc.zip \
            "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-x86_64.zip"
          unzip -q -o /tmp/protoc.zip -d /tmp/protoc
          export PATH="/tmp/protoc/bin:$(go env GOPATH)/bin:$PATH"
          go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
          ./internal/usp/proto/generate.sh
          git diff --exit-code -- internal/usp/uspproto/
        working-directory: backend
```

The `git diff --exit-code` is the whole point: if the committed bindings do not match what the vendored `.proto` generates, CI fails. Pin both versions — an unpinned `protoc` or plugin produces gratuitous diffs.

- [ ] **Step 8: Validate the workflow file**

Run, from the repository root: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"`

Expected: `YAML OK`.

- [ ] **Step 9: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing — note that **generated `.pb.go` files are already gofmt-clean**; if `gofmt -l` names one, do not reformat it, report it, because it means the generator version is wrong. Vet silent; all packages `ok`.

- [ ] **Step 10: Commit**

```bash
git add internal/usp/proto internal/usp/uspproto go.mod go.sum ../.github/workflows/ci.yml
git commit -F - <<'EOF'
feat(usp): vendor the TR-369 schemas and generate protobuf bindings

Vendors usp-record-1-3.proto and usp-msg-1-3.proto as upstream bytes
under BSD-3-Clause, and commits the generated bindings so neither protoc
nor protoc-gen-go is needed to build or test -- only to change the
schema. CI regenerates and fails on any diff, so the committed .pb.go
can never drift from the .proto it came from.

Version 1.3 is the wire contract deliberately: the Broadband Forum
reference agent emits Record.version "1.3" and advertises up to 1.3,
despite its release notes headlining "USP 1.4".

Neither upstream file declares option go_package, so the Go import path
is supplied with protoc -M flags rather than by editing files we do not
own. Both proto packages map to one Go package; they are always used
together and share no top-level type names.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 2: Endpoint IDs

**Why:** A USP Endpoint ID is a routing address, not an identity (spec §5.1). The controller needs to format its own, and to parse an agent's well enough to *optimise* identity lookup — never to establish it, because a database value or the `USP_ENDPOINT_ID` environment variable can override the derived form (spec §3.4, §5.3). Getting that boundary wrong is how you end up with a duplicate fleet.

**Verified facts this encodes** (spec §3.4): obuspa's default is `os::<ManufacturerOUI>-<SerialNumber>`, with both parts percent-encoded leaving alphanumerics and `-._` literal, using uppercase hex. Real examples: `os::012345-0800270B57FF`, and `proto::agent-id` from its CI configs.

**Files:**
- Create: `backend/internal/usp/endpoint.go`
- Test: `backend/internal/usp/endpoint_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (no protobuf types).
- Produces:
  - `type EndpointID string`
  - `func FormatEndpointID(authority, instance string) EndpointID` — joins with `::`, percent-encoding `instance` per the USP rules.
  - `func (e EndpointID) Authority() string` — the part before `::`, or `""` if malformed.
  - `func (e EndpointID) Instance() string` — the part after `::`, percent-decoded, or `""` if malformed.
  - `func (e EndpointID) OUISerial() (ouiSerial string, ok bool)` — returns the whole `<OUI>-<Serial>` instance **only** for a well-formed `os::` endpoint whose instance contains at least one `-`, and neither starts nor ends with one (so both the OUI and the serial are non-empty; a serial may itself contain hyphens); `ok` is false for every other shape.
  - `func PercentEncodeUSP(s string) string` and `func PercentDecodeUSP(s string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/usp/endpoint_test.go`:

```go
package usp

import "testing"

func TestFormatEndpointID(t *testing.T) {
	cases := []struct {
		authority, instance string
		want                EndpointID
	}{
		// The obuspa default shape, from its own documentation.
		{"os", "012345-0800270B57FF", "os::012345-0800270B57FF"},
		// Alphanumerics and -._ stay literal; everything else is encoded
		// with uppercase hex.
		{"os", "a-b.c_d", "os::a-b.c_d"},
		{"os", "has space", "os::has%20space"},
		{"os", "sl/ash", "os::sl%2Fash"},
		{"os", "co:lon", "os::co%3Alon"},
		{"self", "acs-controller", "self::acs-controller"},
	}
	for _, c := range cases {
		if got := FormatEndpointID(c.authority, c.instance); got != c.want {
			t.Errorf("FormatEndpointID(%q, %q) = %q, want %q", c.authority, c.instance, got, c.want)
		}
	}
}

func TestEndpointIDAuthorityAndInstance(t *testing.T) {
	e := EndpointID("os::012345-0800270B57FF")
	if got := e.Authority(); got != "os" {
		t.Errorf("Authority() = %q, want os", got)
	}
	if got := e.Instance(); got != "012345-0800270B57FF" {
		t.Errorf("Instance() = %q, want 012345-0800270B57FF", got)
	}
	// Percent-encoded instances decode.
	if got := EndpointID("os::has%20space").Instance(); got != "has space" {
		t.Errorf("Instance() = %q, want %q", got, "has space")
	}
	// Malformed: no separator.
	if got := EndpointID("nonsense").Authority(); got != "" {
		t.Errorf("Authority() of a malformed id = %q, want empty", got)
	}
	if got := EndpointID("nonsense").Instance(); got != "" {
		t.Errorf("Instance() of a malformed id = %q, want empty", got)
	}
}

// OUISerial is an OPTIMISATION, not an identity source. It must succeed
// only for the exact os:: shape and refuse everything else, so a caller
// cannot accidentally mint a device record from an opaque endpoint id.
func TestOUISerialOnlyForWellFormedOSEndpoints(t *testing.T) {
	ouiSerial, ok := EndpointID("os::012345-0800270B57FF").OUISerial()
	if !ok || ouiSerial != "012345-0800270B57FF" {
		t.Errorf("OUISerial() = (%q, %v), want (012345-0800270B57FF, true)", ouiSerial, ok)
	}

	// A serial containing a hyphen: split on the LAST hyphen, so the OUI
	// is the first component and the serial keeps its own hyphens.
	ouiSerial, ok = EndpointID("os::012345-ABC-123").OUISerial()
	if !ok || ouiSerial != "012345-ABC-123" {
		t.Errorf("OUISerial() = (%q, %v), want the whole instance back", ouiSerial, ok)
	}

	for _, bad := range []EndpointID{
		"proto::agent-id",            // obuspa CI configs use this
		"self::acs-controller",       // controller's own form
		"user::somebody",             // another valid authority scheme
		"os::nohyphen",               // no OUI/serial split
		"os::",                       // empty instance
		"os::-trailing",              // empty OUI component
		"os::trailing-",              // empty serial component
		"os::aa-bb-",                 // multi-hyphen with empty serial: first-hyphen split would wrongly accept
		"os::-aa-bb",                 // multi-hyphen with empty OUI: last-hyphen split would wrongly accept
		"nonsense",                   // no separator at all
		"",                           // empty
	} {
		if got, ok := bad.OUISerial(); ok {
			t.Errorf("OUISerial() on %q returned (%q, true); an identity must not be derivable from it", bad, got)
		}
	}
}

func TestPercentRoundTrip(t *testing.T) {
	for _, s := range []string{"012345-0800270B57FF", "has space", "sl/ash", "a-b.c_d", "%", "100%sure"} {
		enc := PercentEncodeUSP(s)
		dec, err := PercentDecodeUSP(enc)
		if err != nil {
			t.Errorf("PercentDecodeUSP(%q) from %q: %v", enc, s, err)
			continue
		}
		if dec != s {
			t.Errorf("round trip of %q gave %q (encoded %q)", s, dec, enc)
		}
	}
	if _, err := PercentDecodeUSP("bad%zz"); err == nil {
		t.Error("PercentDecodeUSP(\"bad%zz\") returned nil error, want a decode error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestFormatEndpointID|TestEndpointID|TestOUISerial|TestPercent' -v`

Expected: compile FAIL — `undefined: EndpointID`, `undefined: FormatEndpointID`, `undefined: PercentEncodeUSP`, `undefined: PercentDecodeUSP`.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/usp/endpoint.go`:

```go
// Package usp implements the TR-369/USP protocol core: the Record
// envelope, message encoding and decoding, Endpoint IDs and error
// mapping.
//
// It speaks protocol only. It deliberately imports nothing from
// internal/devices, internal/jobs or internal/store (design §4.1), so
// the codec stays independently testable and the wiring between protocol
// and domain lives in cmd/uspc rather than here. boundary_test.go
// enforces that.
package usp

import (
	"fmt"
	"strings"
)

// EndpointID is a USP endpoint address of the form
// "<authority-scheme>::<instance>", e.g. "os::012345-0800270B57FF".
//
// It is a ROUTING ADDRESS, not an identity. Device identity is
// OUI + ProductClass + SerialNumber and is established from a
// Notify.OnBoardRequest or a Get of Device.DeviceInfo -- never from an
// endpoint id alone, because an agent's id can be overridden by a
// database value or an environment variable, and the authority scheme
// may be opaque (proto::, self::, user::). See design §5.1 and §5.3.
type EndpointID string

// endpointSeparator divides the authority scheme from the instance.
const endpointSeparator = "::"

// FormatEndpointID builds an endpoint id, percent-encoding the instance.
func FormatEndpointID(authority, instance string) EndpointID {
	return EndpointID(authority + endpointSeparator + PercentEncodeUSP(instance))
}

// Authority returns the authority scheme, or "" if the id is malformed.
func (e EndpointID) Authority() string {
	authority, _, ok := e.split()
	if !ok {
		return ""
	}
	return authority
}

// Instance returns the percent-decoded instance, or "" if the id is
// malformed or its encoding is invalid.
func (e EndpointID) Instance() string {
	_, instance, ok := e.split()
	if !ok {
		return ""
	}
	decoded, err := PercentDecodeUSP(instance)
	if err != nil {
		return ""
	}
	return decoded
}

func (e EndpointID) split() (authority, instance string, ok bool) {
	authority, instance, found := strings.Cut(string(e), endpointSeparator)
	if !found || authority == "" {
		return "", "", false
	}
	return authority, instance, true
}

// OUISerial extracts an "<OUI>-<SerialNumber>" identity from an endpoint
// id, and reports whether the id had that exact shape.
//
// This is an OPTIMISATION for an agent already in the registry, not a
// way to establish identity. It succeeds only for the "os::" authority
// with an instance that splits into two non-empty parts on its last
// hyphen -- the form the Broadband Forum reference agent derives from
// ManufacturerOUI and SerialNumber. Every other shape returns false, so
// a caller cannot mint a device record from an opaque id and produce a
// duplicate fleet.
func (e EndpointID) OUISerial() (ouiSerial string, ok bool) {
	authority, _, valid := e.split()
	if !valid || authority != "os" {
		return "", false
	}
	instance := e.Instance()
	// Both components must be non-empty. A serial may itself contain
	// hyphens, so neither "first hyphen" nor "last hyphen" alone is the
	// right split: a leading hyphen means an empty OUI, a trailing one an
	// empty serial, and either must be refused.
	if !strings.Contains(instance, "-") ||
		strings.HasPrefix(instance, "-") ||
		strings.HasSuffix(instance, "-") {
		return "", false
	}
	return instance, true
}

// uspSafeChars are the characters obuspa leaves literal when percent-
// encoding an endpoint id component, alongside alphanumerics.
const uspSafeChars = "-._"

// PercentEncodeUSP percent-encodes a string for use as an endpoint id
// instance: alphanumerics and -._ stay literal, everything else becomes
// %XX with uppercase hex.
func PercentEncodeUSP(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUSPUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// PercentDecodeUSP reverses PercentEncodeUSP.
func PercentDecodeUSP(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("truncated percent escape at offset %d in %q", i, s)
		}
		hi, err := unhex(s[i+1])
		if err != nil {
			return "", fmt.Errorf("invalid percent escape at offset %d in %q: %w", i, s, err)
		}
		lo, err := unhex(s[i+2])
		if err != nil {
			return "", fmt.Errorf("invalid percent escape at offset %d in %q: %w", i, s, err)
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), nil
}

func isUSPUnreserved(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte(uspSafeChars, c) >= 0
}

func unhex(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("%q is not a hex digit", c)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/ -v`

Expected: all PASS.

- [ ] **Step 5: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/usp/endpoint.go internal/usp/endpoint_test.go
git commit -F - <<'EOF'
feat(usp): format and parse USP endpoint IDs

An endpoint id is a routing address, not an identity. OUISerial extracts
the OUI-serial pair only from a well-formed os:: id and refuses every
other shape -- proto::, self::, an instance with no hyphen -- so a
caller cannot mint a device record from an opaque id and split one
device into two fleet entries.

Identity itself comes from Notify.OnBoardRequest or a Get of
Device.DeviceInfo in a later plan; this parsing exists only to short-cut
lookup for an agent already registered.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 3: Record envelope

**Files:**
- Create: `backend/internal/usp/record.go`
- Test: `backend/internal/usp/record_test.go`

**Interfaces:**
- Consumes: `acs/internal/usp/uspproto` types from Task 1; `EndpointID` from Task 2.
- Produces:
  - `const RecordVersion = "1.3"` — the version this controller emits.
  - `var SupportedRecordVersions = []string{"1.0", "1.1", "1.2", "1.3"}`
  - `var ErrMalformedRecord = errors.New("malformed USP record")`
  - `var ErrUnsupportedRecordVersion = errors.New("unsupported USP record version")`
  - `var ErrPayloadSecurityUnsupported = errors.New("non-plaintext USP payload security is not supported")`
  - `var ErrNotAddressedToUs = errors.New("USP record is not addressed to this controller")`
  - `var ErrNoPayload = errors.New("USP record carries no message payload")`
  - `func EncodeRecord(from, to EndpointID, payload []byte) ([]byte, error)` — wraps a marshalled `Msg` in a `NoSessionContextRecord` and marshals the `Record`.
  - `type DecodedRecord struct { From, To EndpointID; Version string; Payload []byte }`
  - `func DecodeRecord(wire []byte, us EndpointID) (*DecodedRecord, error)` — unmarshals, validates, and returns the inner payload.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/usp/record_test.go`:

```go
package usp

import (
	"errors"
	"testing"

	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

const (
	testAgent      = EndpointID("os::012345-0800270B57FF")
	testController = EndpointID("self::acs-controller")
)

func TestEncodeDecodeRecordRoundTrip(t *testing.T) {
	wire, err := EncodeRecord(testController, testAgent, []byte("payload-bytes"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	// Decode as the agent would: the record is addressed to it.
	got, err := DecodeRecord(wire, testAgent)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if got.From != testController || got.To != testAgent {
		t.Errorf("addresses = %q -> %q, want %q -> %q", got.From, got.To, testController, testAgent)
	}
	if got.Version != RecordVersion {
		t.Errorf("Version = %q, want %q", got.Version, RecordVersion)
	}
	if string(got.Payload) != "payload-bytes" {
		t.Errorf("Payload = %q, want payload-bytes", got.Payload)
	}
}

// The reference agent emits Record.version "1.3" and advertises up to
// 1.3 despite release notes headlining "USP 1.4". Accepting 1.0-1.3 and
// rejecting anything else is the contract.
func TestDecodeRecordVersionAcceptance(t *testing.T) {
	for _, v := range []string{"1.0", "1.1", "1.2", "1.3"} {
		wire := mustMarshalRecord(t, &uspproto.Record{
			Version: v, ToId: string(testController), FromId: string(testAgent),
			PayloadSecurity: uspproto.Record_PLAINTEXT,
			RecordType: &uspproto.Record_NoSessionContext{
				NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
			},
		})
		if _, err := DecodeRecord(wire, testController); err != nil {
			t.Errorf("version %q rejected: %v", v, err)
		}
	}
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: "2.0", ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrUnsupportedRecordVersion) {
		t.Errorf("version 2.0 gave %v, want ErrUnsupportedRecordVersion", err)
	}
}

// A record addressed to a different endpoint must be refused outright,
// mirroring the reference agent's own check.
func TestDecodeRecordRejectsWrongRecipient(t *testing.T) {
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: RecordVersion, ToId: "os::999999-SOMEONEELSE", FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrNotAddressedToUs) {
		t.Errorf("got %v, want ErrNotAddressedToUs", err)
	}
}

// E2E/encrypted payloads are out of scope and the reference agent
// rejects them too, so we must not silently treat one as plaintext.
func TestDecodeRecordRejectsNonPlaintextSecurity(t *testing.T) {
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_TLS12,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrPayloadSecurityUnsupported) {
		t.Errorf("got %v, want ErrPayloadSecurityUnsupported", err)
	}
}

// A connect or disconnect record carries no message. Callers must be
// able to tell that apart from a decode failure.
func TestDecodeRecordNoPayload(t *testing.T) {
	for name, rt := range map[string]any{
		"websocket_connect": &uspproto.Record_WebsocketConnect{WebsocketConnect: &uspproto.WebSocketConnectRecord{}},
		"disconnect":        &uspproto.Record_Disconnect{Disconnect: &uspproto.DisconnectRecord{Reason: "bye"}},
		// A NoSessionContext record whose payload is empty is a distinct
		// branch from a connect/disconnect record and must also be ErrNoPayload.
		"empty_no_session_context": &uspproto.Record_NoSessionContext{NoSessionContext: &uspproto.NoSessionContextRecord{Payload: nil}},
	} {
		rec := &uspproto.Record{
			Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
			PayloadSecurity: uspproto.Record_PLAINTEXT,
		}
		switch v := rt.(type) {
		case *uspproto.Record_WebsocketConnect:
			rec.RecordType = v
		case *uspproto.Record_Disconnect:
			rec.RecordType = v
		case *uspproto.Record_NoSessionContext:
			rec.RecordType = v
		}
		if _, err := DecodeRecord(mustMarshalRecord(t, rec), testController); !errors.Is(err, ErrNoPayload) {
			t.Errorf("%s: got %v, want ErrNoPayload", name, err)
		}
	}
}

func TestDecodeRecordGarbage(t *testing.T) {
	if _, err := DecodeRecord([]byte{0xff, 0xff, 0xff, 0xff}, testController); !errors.Is(err, ErrMalformedRecord) {
		t.Errorf("got %v, want ErrMalformedRecord", err)
	}
}

func mustMarshalRecord(t *testing.T, r *uspproto.Record) []byte {
	t.Helper()
	wire, err := proto.Marshal(r)
	if err != nil {
		t.Fatalf("marshal test record: %v", err)
	}
	return wire
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run TestEncodeDecodeRecord -v` and `go test ./internal/usp/ -run TestDecodeRecord -v`

Expected: compile FAIL — `undefined: EncodeRecord`, `undefined: DecodeRecord`, `undefined: RecordVersion`, and the error variables.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/usp/record.go`:

```go
package usp

import (
	"errors"
	"fmt"
	"slices"

	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// RecordVersion is the USP version this controller stamps on every
// record it sends.
//
// 1.3 deliberately, not 1.4 or 1.5: the Broadband Forum reference agent
// emits Record.version "1.3" and advertises "1.0,1.1,1.2,1.3" even in
// releases whose notes headline "USP 1.4" (design §3.4). A controller
// that demanded 1.4 would fail against the reference implementation.
const RecordVersion = "1.3"

// SupportedRecordVersions are the versions this controller accepts on
// inbound records.
var SupportedRecordVersions = []string{"1.0", "1.1", "1.2", "1.3"}

var (
	ErrMalformedRecord            = errors.New("malformed USP record")
	ErrUnsupportedRecordVersion   = errors.New("unsupported USP record version")
	ErrPayloadSecurityUnsupported = errors.New("non-plaintext USP payload security is not supported")
	ErrNotAddressedToUs           = errors.New("USP record is not addressed to this controller")
	ErrNoPayload                  = errors.New("USP record carries no message payload")
)

// EncodeRecord wraps a marshalled Msg in a Record addressed from us to
// the agent.
//
// Always a NoSessionContextRecord: session context exists for
// segmentation and end-to-end encryption, both of which are out of scope
// (design §10) and rejected by the reference agent anyway.
func EncodeRecord(from, to EndpointID, payload []byte) ([]byte, error) {
	wire, err := proto.Marshal(&uspproto.Record{
		Version:         RecordVersion,
		ToId:            string(to),
		FromId:          string(from),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: payload},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal USP record: %w", err)
	}
	return wire, nil
}

// DecodedRecord is a validated inbound record with its message payload
// extracted.
type DecodedRecord struct {
	From    EndpointID
	To      EndpointID
	Version string
	Payload []byte
}

// DecodeRecord unmarshals and validates an inbound record, returning the
// inner message payload.
//
// us is this controller's own endpoint id. A record addressed elsewhere
// is refused rather than processed: the reference agent applies the same
// rule in the other direction, and honouring a misaddressed record would
// make the endpoint id meaningless as an access control.
//
// A connect or disconnect record is well-formed but carries no message;
// callers get ErrNoPayload so they can treat it as a lifecycle event
// rather than a decode failure.
func DecodeRecord(wire []byte, us EndpointID) (*DecodedRecord, error) {
	var rec uspproto.Record
	if err := proto.Unmarshal(wire, &rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedRecord, err)
	}
	if !slices.Contains(SupportedRecordVersions, rec.GetVersion()) {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedRecordVersion, rec.GetVersion())
	}
	if rec.GetPayloadSecurity() != uspproto.Record_PLAINTEXT {
		return nil, fmt.Errorf("%w: %v", ErrPayloadSecurityUnsupported, rec.GetPayloadSecurity())
	}
	if rec.GetToId() != string(us) {
		return nil, fmt.Errorf("%w: addressed to %q, we are %q", ErrNotAddressedToUs, rec.GetToId(), us)
	}
	nsc, ok := rec.GetRecordType().(*uspproto.Record_NoSessionContext)
	if !ok {
		return nil, fmt.Errorf("%w: record type %T", ErrNoPayload, rec.GetRecordType())
	}
	payload := nsc.NoSessionContext.GetPayload()
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty no-session-context payload", ErrNoPayload)
	}
	return &DecodedRecord{
		From:    EndpointID(rec.GetFromId()),
		To:      EndpointID(rec.GetToId()),
		Version: rec.GetVersion(),
		Payload: payload,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/ -v`

Expected: all PASS.

- [ ] **Step 5: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/usp/record.go internal/usp/record_test.go
git commit -F - <<'EOF'
feat(usp): encode and decode the USP Record envelope

Emits version 1.3 and accepts 1.0-1.3: the reference agent emits "1.3"
and advertises up to 1.3 even in releases headlining "USP 1.4", so
demanding 1.4 would fail against it.

A record addressed to another endpoint is refused rather than processed
-- honouring a misaddressed record would make the endpoint id
meaningless as an access control. Non-plaintext payload security is
refused too: end-to-end encryption is out of scope and the reference
agent rejects it as well, so silently treating one as plaintext would be
worse than failing.

Connect and disconnect records are well-formed but carry no message, and
report ErrNoPayload so a caller can tell a lifecycle event from a decode
failure.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 4: Message envelope and request encoding

**Files:**
- Create: `backend/internal/usp/message.go`
- Test: `backend/internal/usp/message_test.go`

**Interfaces:**
- Consumes: `uspproto` types from Task 1.
- Produces:
  - `var ErrMalformedMessage = errors.New("malformed USP message")`
  - `func NewMsgID() string` — a fresh correlation id, `uuid.NewString()`.
  - `func EncodeGet(msgID string, paths []string, maxDepth uint32) ([]byte, error)`
  - `func EncodeSet(msgID string, allowPartial bool, updates map[string]map[string]string) ([]byte, error)` — outer key is an object path, inner map is parameter name to value.
  - `func EncodeAdd(msgID string, allowPartial bool, objPath string, params map[string]string) ([]byte, error)`
  - `func EncodeDelete(msgID string, allowPartial bool, objPaths []string) ([]byte, error)`
  - `func EncodeOperate(msgID, command, commandKey string, sendResp bool, inputArgs map[string]string) ([]byte, error)`
  - `func EncodeGetSupportedDM(msgID string, objPaths []string, firstLevelOnly, returnCommands, returnEvents, returnParams bool) ([]byte, error)`
  - `func EncodeGetInstances(msgID string, objPaths []string, firstLevelOnly bool) ([]byte, error)`
  - `func EncodeGetSupportedProtocol(msgID, controllerSupportedProtocolVersions string) ([]byte, error)`
  - `func DecodeMsg(payload []byte) (*uspproto.Msg, error)`

**A note on `Operate`'s `command_key`.** This is the field that later carries job correlation: `Notify.OperationComplete` echoes it, and the existing job queue already correlates CWMP `TransferComplete` on a `command_key` (spec §3.3, §6.3). Encode it faithfully; do not generate it here — the caller supplies the job's own key.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/usp/message_test.go`:

```go
package usp

import (
	"errors"
	"testing"

	"acs/internal/usp/uspproto"
)

func TestNewMsgIDIsUnique(t *testing.T) {
	a, b := NewMsgID(), NewMsgID()
	if a == "" || b == "" {
		t.Fatal("NewMsgID returned an empty id")
	}
	if a == b {
		t.Errorf("NewMsgID returned the same id twice: %q", a)
	}
}

func TestEncodeGet(t *testing.T) {
	wire, err := EncodeGet("m-1", []string{"Device.DeviceInfo.", "Device.WiFi."}, 2)
	if err != nil {
		t.Fatalf("EncodeGet: %v", err)
	}
	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	if msg.GetHeader().GetMsgId() != "m-1" {
		t.Errorf("msg_id = %q, want m-1", msg.GetHeader().GetMsgId())
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_GET {
		t.Errorf("msg_type = %v, want GET", msg.GetHeader().GetMsgType())
	}
	get := msg.GetBody().GetRequest().GetGet()
	if len(get.GetParamPaths()) != 2 || get.GetParamPaths()[0] != "Device.DeviceInfo." {
		t.Errorf("param_paths = %v", get.GetParamPaths())
	}
	if get.GetMaxDepth() != 2 {
		t.Errorf("max_depth = %d, want 2", get.GetMaxDepth())
	}
}

func TestEncodeSet(t *testing.T) {
	wire, err := EncodeSet("m-2", false, map[string]map[string]string{
		"Device.WiFi.SSID.1.": {"SSID": "acs-test"},
	})
	if err != nil {
		t.Fatalf("EncodeSet: %v", err)
	}
	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_SET {
		t.Errorf("msg_type = %v, want SET", msg.GetHeader().GetMsgType())
	}
	set := msg.GetBody().GetRequest().GetSet()
	if set.GetAllowPartial() {
		t.Error("allow_partial = true, want false")
	}
	objs := set.GetUpdateObjs()
	if len(objs) != 1 || objs[0].GetObjPath() != "Device.WiFi.SSID.1." {
		t.Fatalf("update_objs = %+v", objs)
	}
	params := objs[0].GetParamSettings()
	if len(params) != 1 || params[0].GetParam() != "SSID" || params[0].GetValue() != "acs-test" {
		t.Errorf("param_settings = %+v", params)
	}
}

func TestEncodeAddDeleteOperate(t *testing.T) {
	addWire, err := EncodeAdd("m-3", true, "Device.WiFi.SSID.", map[string]string{"SSID": "guest"})
	if err != nil {
		t.Fatalf("EncodeAdd: %v", err)
	}
	addMsg, err := DecodeMsg(addWire)
	if err != nil {
		t.Fatal(err)
	}
	if addMsg.GetHeader().GetMsgType() != uspproto.Header_ADD {
		t.Errorf("add msg_type = %v", addMsg.GetHeader().GetMsgType())
	}
	if !addMsg.GetBody().GetRequest().GetAdd().GetAllowPartial() {
		t.Error("add allow_partial = false, want true")
	}

	delWire, err := EncodeDelete("m-4", false, []string{"Device.WiFi.SSID.2."})
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delMsg, err := DecodeMsg(delWire)
	if err != nil {
		t.Fatal(err)
	}
	if delMsg.GetHeader().GetMsgType() != uspproto.Header_DELETE {
		t.Errorf("delete msg_type = %v", delMsg.GetHeader().GetMsgType())
	}
	if paths := delMsg.GetBody().GetRequest().GetDelete().GetObjPaths(); len(paths) != 1 || paths[0] != "Device.WiFi.SSID.2." {
		t.Errorf("delete obj_paths = %v", paths)
	}

	// command_key is the field OperationComplete echoes back, and it is
	// how an async operation correlates to a job. It must survive
	// encoding exactly as given.
	opWire, err := EncodeOperate("m-5", "Device.Reboot()", "job-abc-123", true, nil)
	if err != nil {
		t.Fatalf("EncodeOperate: %v", err)
	}
	opMsg, err := DecodeMsg(opWire)
	if err != nil {
		t.Fatal(err)
	}
	op := opMsg.GetBody().GetRequest().GetOperate()
	if op.GetCommand() != "Device.Reboot()" {
		t.Errorf("command = %q", op.GetCommand())
	}
	if op.GetCommandKey() != "job-abc-123" {
		t.Errorf("command_key = %q, want job-abc-123 -- async job correlation depends on this", op.GetCommandKey())
	}
	if !op.GetSendResp() {
		t.Error("send_resp = false, want true")
	}
}

func TestEncodeDiscoveryMessages(t *testing.T) {
	dmWire, err := EncodeGetSupportedDM("m-6", []string{"Device."}, false, true, true, true)
	if err != nil {
		t.Fatalf("EncodeGetSupportedDM: %v", err)
	}
	dmMsg, err := DecodeMsg(dmWire)
	if err != nil {
		t.Fatal(err)
	}
	if dmMsg.GetHeader().GetMsgType() != uspproto.Header_GET_SUPPORTED_DM {
		t.Errorf("msg_type = %v", dmMsg.GetHeader().GetMsgType())
	}
	dm := dmMsg.GetBody().GetRequest().GetGetSupportedDm()
	if dm.GetFirstLevelOnly() || !dm.GetReturnCommands() || !dm.GetReturnEvents() || !dm.GetReturnParams() {
		t.Errorf("flags = %+v", dm)
	}

	instWire, err := EncodeGetInstances("m-7", []string{"Device.WiFi.SSID."}, true)
	if err != nil {
		t.Fatalf("EncodeGetInstances: %v", err)
	}
	instMsg, err := DecodeMsg(instWire)
	if err != nil {
		t.Fatal(err)
	}
	if instMsg.GetHeader().GetMsgType() != uspproto.Header_GET_INSTANCES {
		t.Errorf("msg_type = %v", instMsg.GetHeader().GetMsgType())
	}
	if !instMsg.GetBody().GetRequest().GetGetInstances().GetFirstLevelOnly() {
		t.Error("first_level_only = false, want true")
	}

	protoWire, err := EncodeGetSupportedProtocol("m-8", "1.0,1.1,1.2,1.3")
	if err != nil {
		t.Fatalf("EncodeGetSupportedProtocol: %v", err)
	}
	protoMsg, err := DecodeMsg(protoWire)
	if err != nil {
		t.Fatal(err)
	}
	if protoMsg.GetHeader().GetMsgType() != uspproto.Header_GET_SUPPORTED_PROTO {
		t.Errorf("msg_type = %v", protoMsg.GetHeader().GetMsgType())
	}
	if got := protoMsg.GetBody().GetRequest().GetGetSupportedProtocol().GetControllerSupportedProtocolVersions(); got != "1.0,1.1,1.2,1.3" {
		t.Errorf("controller_supported_protocol_versions = %q", got)
	}
}

func TestDecodeMsgGarbage(t *testing.T) {
	if _, err := DecodeMsg([]byte{0xff, 0xff, 0xff, 0xff}); !errors.Is(err, ErrMalformedMessage) {
		t.Errorf("got %v, want ErrMalformedMessage", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestNewMsgID|TestEncode|TestDecodeMsg' -v`

Expected: compile FAIL — `undefined: NewMsgID`, `undefined: EncodeGet`, and the rest.

- [ ] **Step 3: Confirm the generated field and getter names**

The tests above assume protoc-gen-go's casing (`GetGetSupportedDm`, `GetUpdateObjs`, `GetParamSettings`, `GetControllerSupportedProtocolVersions`). Before writing the implementation, check them against the generated code:

```bash
cd backend
grep -oE "func \(x \*(Set|Add|Delete|Operate|GetSupportedDM|GetInstances|GetSupportedProtocol)\) Get[A-Za-z]+\(\)" internal/usp/uspproto/usp-msg-1-3.pb.go | sort -u
grep -oE "func \(x \*Request\) Get[A-Za-z]+\(\)" internal/usp/uspproto/usp-msg-1-3.pb.go | sort -u
grep -oE "type Set_UpdateObject struct|type Set_UpdateObject_UpdateParamSetting struct" internal/usp/uspproto/usp-msg-1-3.pb.go
```

Generated names win over the plan's guesses. Where they differ, fix the **test** to match the generated code and note each difference in your report — do not rename generated types.

- [ ] **Step 4: Write the implementation**

Create `backend/internal/usp/message.go`:

```go
package usp

import (
	"errors"
	"fmt"

	"acs/internal/usp/uspproto"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// ErrMalformedMessage is returned when an inbound payload is not a
// decodable USP Msg.
var ErrMalformedMessage = errors.New("malformed USP message")

// NewMsgID returns a fresh message correlation id. USP requires msg_id
// to be unique per outstanding request from a given endpoint; a UUID is
// the cheapest way to guarantee that without shared state.
func NewMsgID() string { return uuid.NewString() }

// encodeRequest marshals one request into a Msg with the given header.
func encodeRequest(msgID string, msgType uspproto.Header_MsgType, req *uspproto.Request) ([]byte, error) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: msgType},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: req}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal USP %v: %w", msgType, err)
	}
	return wire, nil
}

// EncodeGet builds a Get. maxDepth 0 means unlimited.
func EncodeGet(msgID string, paths []string, maxDepth uint32) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET, &uspproto.Request{
		ReqType: &uspproto.Request_Get{Get: &uspproto.Get{
			ParamPaths: paths,
			MaxDepth:   maxDepth,
		}},
	})
}

// EncodeSet builds a Set. updates maps an object path to the parameters
// being written on it.
//
// allowPartial false means the agent applies all of it or none, which is
// what a configuration write generally wants: a half-applied Set leaves
// a device in a state no operator asked for.
func EncodeSet(msgID string, allowPartial bool, updates map[string]map[string]string) ([]byte, error) {
	objs := make([]*uspproto.Set_UpdateObject, 0, len(updates))
	for objPath, params := range updates {
		settings := make([]*uspproto.Set_UpdateParamSetting, 0, len(params))
		for name, value := range params {
			settings = append(settings, &uspproto.Set_UpdateParamSetting{
				Param:    name,
				Value:    value,
				Required: true,
			})
		}
		objs = append(objs, &uspproto.Set_UpdateObject{
			ObjPath:       objPath,
			ParamSettings: settings,
		})
	}
	return encodeRequest(msgID, uspproto.Header_SET, &uspproto.Request{
		ReqType: &uspproto.Request_Set{Set: &uspproto.Set{
			AllowPartial: allowPartial,
			UpdateObjs:   objs,
		}},
	})
}

// EncodeAdd builds an Add creating one instance of a multi-instance
// object, with optional initial parameter values.
func EncodeAdd(msgID string, allowPartial bool, objPath string, params map[string]string) ([]byte, error) {
	settings := make([]*uspproto.Add_CreateParamSetting, 0, len(params))
	for name, value := range params {
		settings = append(settings, &uspproto.Add_CreateParamSetting{
			Param:    name,
			Value:    value,
			Required: true,
		})
	}
	return encodeRequest(msgID, uspproto.Header_ADD, &uspproto.Request{
		ReqType: &uspproto.Request_Add{Add: &uspproto.Add{
			AllowPartial: allowPartial,
			CreateObjs: []*uspproto.Add_CreateObject{{
				ObjPath:       objPath,
				ParamSettings: settings,
			}},
		}},
	})
}

// EncodeDelete builds a Delete over one or more object instances.
func EncodeDelete(msgID string, allowPartial bool, objPaths []string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_DELETE, &uspproto.Request{
		ReqType: &uspproto.Request_Delete{Delete: &uspproto.Delete{
			AllowPartial: allowPartial,
			ObjPaths:     objPaths,
		}},
	})
}

// EncodeOperate builds an Operate invoking a data-model command.
//
// commandKey is the caller's correlation token, echoed back in
// Notify.OperationComplete for an asynchronous command. It is supplied
// rather than generated here because it must match the job the operation
// belongs to -- the same field CWMP's TransferComplete already
// correlates on (design §3.3, §6.3).
func EncodeOperate(msgID, command, commandKey string, sendResp bool, inputArgs map[string]string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_OPERATE, &uspproto.Request{
		ReqType: &uspproto.Request_Operate{Operate: &uspproto.Operate{
			Command:    command,
			CommandKey: commandKey,
			SendResp:   sendResp,
			InputArgs:  inputArgs,
		}},
	})
}

// EncodeGetSupportedDM builds a GetSupportedDM, the message that reports
// each parameter's access type and each command's synchronicity -- so
// writability and sync/async are discoverable rather than guessed
// (design §3.3).
func EncodeGetSupportedDM(msgID string, objPaths []string, firstLevelOnly, returnCommands, returnEvents, returnParams bool) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_SUPPORTED_DM, &uspproto.Request{
		ReqType: &uspproto.Request_GetSupportedDm{GetSupportedDm: &uspproto.GetSupportedDM{
			ObjPaths:       objPaths,
			FirstLevelOnly: firstLevelOnly,
			ReturnCommands: returnCommands,
			ReturnEvents:   returnEvents,
			ReturnParams:   returnParams,
		}},
	})
}

// EncodeGetInstances builds a GetInstances.
func EncodeGetInstances(msgID string, objPaths []string, firstLevelOnly bool) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_INSTANCES, &uspproto.Request{
		ReqType: &uspproto.Request_GetInstances{GetInstances: &uspproto.GetInstances{
			ObjPaths:       objPaths,
			FirstLevelOnly: firstLevelOnly,
		}},
	})
}

// EncodeGetSupportedProtocol builds a GetSupportedProtocol advertising
// the versions this controller speaks.
func EncodeGetSupportedProtocol(msgID, controllerSupportedProtocolVersions string) ([]byte, error) {
	return encodeRequest(msgID, uspproto.Header_GET_SUPPORTED_PROTO, &uspproto.Request{
		ReqType: &uspproto.Request_GetSupportedProtocol{GetSupportedProtocol: &uspproto.GetSupportedProtocol{
			ControllerSupportedProtocolVersions: controllerSupportedProtocolVersions,
		}},
	})
}

// DecodeMsg unmarshals a record payload into a Msg.
func DecodeMsg(payload []byte) (*uspproto.Msg, error) {
	var msg uspproto.Msg
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}
	if msg.GetHeader() == nil || msg.GetBody() == nil {
		return nil, fmt.Errorf("%w: header or body absent", ErrMalformedMessage)
	}
	return &msg, nil
}
```

**If a generated type or field name differs** from the above (for instance `Set_UpdateParamSetting` nested differently, or `InputArgs` not being a `map[string]string`), adjust the implementation to the generated names, keep the exported function signatures in the **Interfaces** block unchanged, and record each difference in your report. The signatures are what later plans depend on.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/usp/ -v`

Expected: all PASS.

- [ ] **Step 6: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean. `go.mod` gains no new module — `github.com/google/uuid` is already a direct dependency.

- [ ] **Step 7: Commit**

```bash
git add internal/usp/message.go internal/usp/message_test.go
git commit -F - <<'EOF'
feat(usp): encode USP requests and decode the message envelope

Covers the request set this controller needs: Get, Set, Add, Delete,
Operate, GetSupportedDM, GetInstances and GetSupportedProtocol.

Operate's command_key is supplied by the caller rather than generated
here, because it must match the job the operation belongs to --
Notify.OperationComplete echoes it back, which is the same correlation
path CWMP's TransferComplete already uses.

Set defaults to all-or-nothing rather than partial application: a
half-applied configuration write leaves a device in a state no operator
asked for.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 5: USP error codes as typed errors

**Verified fact:** the USP error table is 7000–7026 (spec §3.3). Four map onto meanings later plans act on specifically (spec §6.4): `7013` non-writeable parameter — the typed equivalent of the Huawei-class trap that is silent on CWMP; `7016` object does not exist; `7006` permission denied, which indicates a controller-trust misconfiguration rather than a device fault; `7022` command failure.

**Files:**
- Create: `backend/internal/usp/errors.go`
- Test: `backend/internal/usp/errors_test.go`

**Interfaces:**
- Consumes: `uspproto` types from Task 1.
- Produces:
  - `type ErrorCode uint32` with constants `ErrCodeMessageFailed = 7000`, `ErrCodeMessageNotSupported = 7001`, `ErrCodeRequestDenied = 7002`, `ErrCodeInternalError = 7003`, `ErrCodeInvalidArguments = 7004`, `ErrCodeResourcesExceeded = 7005`, `ErrCodePermissionDenied = 7006`, `ErrCodeInvalidConfiguration = 7007`, `ErrCodeInvalidPathSyntax = 7008`, `ErrCodeParamActionFailed = 7009`, `ErrCodeUnsupportedParam = 7010`, `ErrCodeInvalidType = 7011`, `ErrCodeInvalidValue = 7012`, `ErrCodeNotWriteable = 7013`, `ErrCodeValueConflict = 7014`, `ErrCodeOperationError = 7015`, `ErrCodeObjectDoesNotExist = 7016`, `ErrCodeObjectNotCreatable = 7017`, `ErrCodeNotATable = 7018`, `ErrCodeObjectNotCreatableNC = 7019`, `ErrCodeObjectNotUpdatable = 7020`, `ErrCodeRequiredParamFailed = 7021`, `ErrCodeCommandFailure = 7022`, `ErrCodeCommandCanceled = 7023`, `ErrCodeDeleteFailure = 7024`, `ErrCodeDuplicateKey = 7025`
  - `func (c ErrorCode) String() string`
  - `type USPError struct { Code ErrorCode; Message string; ParamErrors []ParamError }` implementing `error`
  - `type ParamError struct { Path string; Code ErrorCode; Message string }`
  - `func ErrorFromMsg(msg *uspproto.Msg) *USPError` — returns nil when the message is not an Error body.
  - Sentinels for the four that later plans branch on: `var ErrNotWriteable`, `ErrObjectDoesNotExist`, `ErrPermissionDenied`, `ErrCommandFailure`, each matchable with `errors.Is` against a `*USPError` carrying that code.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/usp/errors_test.go`:

```go
package usp

import (
	"errors"
	"strings"
	"testing"

	"acs/internal/usp/uspproto"
)

func TestErrorCodeString(t *testing.T) {
	cases := map[ErrorCode]string{
		ErrCodeNotWriteable:       "attempt to update non-writeable parameter",
		ErrCodeObjectDoesNotExist: "object does not exist",
		ErrCodePermissionDenied:   "permission denied",
		ErrCodeCommandFailure:     "command failure",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("ErrorCode(%d).String() = %q, want %q", code, got, want)
		}
	}
	// An unmapped code must still render usefully rather than blankly.
	if got := ErrorCode(7999).String(); !strings.Contains(got, "7999") {
		t.Errorf("unknown code rendered as %q, want it to mention 7999", got)
	}
}

func TestErrorFromMsg(t *testing.T) {
	msg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-1", MsgType: uspproto.Header_ERROR},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{
			ErrCode: uint32(ErrCodeNotWriteable),
			ErrMsg:  "KeyPassphrase is read-only",
			ParamErrs: []*uspproto.Error_ParamError{{
				ParamPath: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
				ErrCode:   uint32(ErrCodeNotWriteable),
				ErrMsg:    "read-only",
			}},
		}}},
	}
	uspErr := ErrorFromMsg(msg)
	if uspErr == nil {
		t.Fatal("ErrorFromMsg returned nil for an Error body")
	}
	if uspErr.Code != ErrCodeNotWriteable {
		t.Errorf("Code = %d, want %d", uspErr.Code, ErrCodeNotWriteable)
	}
	if uspErr.Message != "KeyPassphrase is read-only" {
		t.Errorf("Message = %q", uspErr.Message)
	}
	if len(uspErr.ParamErrors) != 1 || uspErr.ParamErrors[0].Path != "Device.WiFi.AccessPoint.1.Security.KeyPassphrase" {
		t.Errorf("ParamErrors = %+v", uspErr.ParamErrors)
	}
	// The error string must carry the code and message, since it is what
	// lands in a job's failure detail.
	if s := uspErr.Error(); !strings.Contains(s, "7013") || !strings.Contains(s, "read-only") {
		t.Errorf("Error() = %q, want it to mention 7013 and the message", s)
	}
}

func TestErrorFromMsgNilForNonError(t *testing.T) {
	msg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: "m-2", MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{}},
		}}},
	}
	if got := ErrorFromMsg(msg); got != nil {
		t.Errorf("ErrorFromMsg on a GetResp = %+v, want nil", got)
	}
	if got := ErrorFromMsg(nil); got != nil {
		t.Errorf("ErrorFromMsg(nil) = %+v, want nil", got)
	}
}

// The four codes later plans branch on must be matchable with errors.Is,
// so a dispatcher can say "this was a non-writeable parameter" without
// comparing integers at the call site.
func TestSentinelMatching(t *testing.T) {
	cases := []struct {
		code     ErrorCode
		sentinel error
	}{
		{ErrCodeNotWriteable, ErrNotWriteable},
		{ErrCodeObjectDoesNotExist, ErrObjectDoesNotExist},
		{ErrCodePermissionDenied, ErrPermissionDenied},
		{ErrCodeCommandFailure, ErrCommandFailure},
	}
	for _, c := range cases {
		err := error(&USPError{Code: c.code, Message: "x"})
		if !errors.Is(err, c.sentinel) {
			t.Errorf("USPError{Code: %d} does not match its sentinel", c.code)
		}
		// And must not match a different sentinel.
		for _, other := range cases {
			if other.code == c.code {
				continue
			}
			if errors.Is(err, other.sentinel) {
				t.Errorf("USPError{Code: %d} wrongly matches the sentinel for %d", c.code, other.code)
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestErrorCode|TestErrorFromMsg|TestSentinel' -v`

Expected: compile FAIL — `undefined: ErrorCode`, `undefined: USPError`, `undefined: ErrorFromMsg`, and the sentinels.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/usp/errors.go`:

```go
package usp

import (
	"errors"
	"fmt"

	"acs/internal/usp/uspproto"
)

// ErrorCode is a USP error code. The table runs 7000-7026 (design §3.3).
type ErrorCode uint32

const (
	ErrCodeMessageFailed        ErrorCode = 7000
	ErrCodeMessageNotSupported  ErrorCode = 7001
	ErrCodeRequestDenied        ErrorCode = 7002
	ErrCodeInternalError        ErrorCode = 7003
	ErrCodeInvalidArguments     ErrorCode = 7004
	ErrCodeResourcesExceeded    ErrorCode = 7005
	ErrCodePermissionDenied     ErrorCode = 7006
	ErrCodeInvalidConfiguration ErrorCode = 7007
	ErrCodeInvalidPathSyntax    ErrorCode = 7008
	ErrCodeParamActionFailed    ErrorCode = 7009
	ErrCodeUnsupportedParam     ErrorCode = 7010
	ErrCodeInvalidType          ErrorCode = 7011
	ErrCodeInvalidValue         ErrorCode = 7012
	ErrCodeNotWriteable         ErrorCode = 7013
	ErrCodeValueConflict        ErrorCode = 7014
	ErrCodeOperationError       ErrorCode = 7015
	ErrCodeObjectDoesNotExist   ErrorCode = 7016
	ErrCodeObjectNotCreatable   ErrorCode = 7017
	ErrCodeNotATable            ErrorCode = 7018
	ErrCodeObjectNotCreatableNC ErrorCode = 7019
	ErrCodeObjectNotUpdatable   ErrorCode = 7020
	ErrCodeRequiredParamFailed  ErrorCode = 7021
	ErrCodeCommandFailure       ErrorCode = 7022
	ErrCodeCommandCanceled      ErrorCode = 7023
	ErrCodeDeleteFailure        ErrorCode = 7024
	ErrCodeDuplicateKey         ErrorCode = 7025
)

var errorCodeText = map[ErrorCode]string{
	ErrCodeMessageFailed:        "message failed",
	ErrCodeMessageNotSupported:  "message not supported",
	ErrCodeRequestDenied:        "request denied",
	ErrCodeInternalError:        "internal error",
	ErrCodeInvalidArguments:     "invalid arguments",
	ErrCodeResourcesExceeded:    "resources exceeded",
	ErrCodePermissionDenied:     "permission denied",
	ErrCodeInvalidConfiguration: "invalid configuration",
	ErrCodeInvalidPathSyntax:    "invalid path syntax",
	ErrCodeParamActionFailed:    "parameter action failed",
	ErrCodeUnsupportedParam:     "unsupported parameter",
	ErrCodeInvalidType:          "invalid type",
	ErrCodeInvalidValue:         "invalid value",
	ErrCodeNotWriteable:         "attempt to update non-writeable parameter",
	ErrCodeValueConflict:        "value conflict",
	ErrCodeOperationError:       "operation error",
	ErrCodeObjectDoesNotExist:   "object does not exist",
	ErrCodeObjectNotCreatable:   "object could not be created",
	ErrCodeNotATable:            "object is not a table",
	ErrCodeObjectNotCreatableNC: "attempt to create non-creatable object",
	ErrCodeObjectNotUpdatable:   "object could not be updated",
	ErrCodeRequiredParamFailed:  "required parameter failed",
	ErrCodeCommandFailure:       "command failure",
	ErrCodeCommandCanceled:      "command canceled",
	ErrCodeDeleteFailure:        "delete failure",
	ErrCodeDuplicateKey:         "object exists with duplicate key",
}

// String renders a code's meaning, falling back to the number so an
// unmapped code from a future USP version is still legible in a log or a
// job's failure detail.
func (c ErrorCode) String() string {
	if text, ok := errorCodeText[c]; ok {
		return text
	}
	return fmt.Sprintf("USP error %d", uint32(c))
}

// Sentinels for the codes later plans branch on (design §6.4). Match
// them with errors.Is rather than comparing integers at the call site.
//
// ErrNotWriteable is worth singling out: on CWMP a non-writeable
// parameter is the Huawei-class trap that resolves, sends and silently
// fails. On USP it is a typed error, which is a real improvement in
// diagnosability.
var (
	ErrNotWriteable       = errors.New("usp: attempt to update non-writeable parameter")
	ErrObjectDoesNotExist = errors.New("usp: object does not exist")
	ErrPermissionDenied   = errors.New("usp: permission denied")
	ErrCommandFailure     = errors.New("usp: command failure")
)

// sentinelForCode maps a code to its sentinel, for errors.Is.
var sentinelForCode = map[ErrorCode]error{
	ErrCodeNotWriteable:       ErrNotWriteable,
	ErrCodeObjectDoesNotExist: ErrObjectDoesNotExist,
	ErrCodePermissionDenied:   ErrPermissionDenied,
	ErrCodeCommandFailure:     ErrCommandFailure,
}

// ParamError is a per-parameter failure inside a USP error.
type ParamError struct {
	Path    string
	Code    ErrorCode
	Message string
}

// USPError is an error returned by an agent.
type USPError struct {
	Code        ErrorCode
	Message     string
	ParamErrors []ParamError
}

func (e *USPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("usp error %d (%s)", uint32(e.Code), e.Code)
	}
	return fmt.Sprintf("usp error %d (%s): %s", uint32(e.Code), e.Code, e.Message)
}

// Is lets errors.Is match a USPError against the sentinel for its code.
func (e *USPError) Is(target error) bool {
	return sentinelForCode[e.Code] == target && target != nil
}

// ErrorFromMsg extracts a USPError from a message, or nil when the
// message is not an Error body.
func ErrorFromMsg(msg *uspproto.Msg) *USPError {
	if msg == nil {
		return nil
	}
	body, ok := msg.GetBody().GetMsgBody().(*uspproto.Body_Error)
	if !ok || body.Error == nil {
		return nil
	}
	out := &USPError{
		Code:    ErrorCode(body.Error.GetErrCode()),
		Message: body.Error.GetErrMsg(),
	}
	for _, pe := range body.Error.GetParamErrs() {
		out.ParamErrors = append(out.ParamErrors, ParamError{
			Path:    pe.GetParamPath(),
			Code:    ErrorCode(pe.GetErrCode()),
			Message: pe.GetErrMsg(),
		})
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/ -v`

Expected: all PASS.

- [ ] **Step 5: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/usp/errors.go internal/usp/errors_test.go
git commit -F - <<'EOF'
feat(usp): map USP error codes to typed Go errors

Covers 7000-7025, with errors.Is sentinels for the four a dispatcher
branches on: non-writeable parameter, object does not exist, permission
denied and command failure.

7013 is the one worth naming: on CWMP a non-writeable parameter is the
trap that resolves, sends and silently fails, which is exactly the
defect this platform hit on Huawei TR-098 devices. On USP it arrives as
a typed error, so it can be surfaced rather than guessed at.

An unmapped code still renders with its number, so a future USP
version's error is legible in a log rather than blank.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 6: Enforce the package boundary

**Why:** Spec §4.1 requires `internal/usp` to speak protocol only and never import `devices`, `jobs` or `store`. That keeps the codec independently testable and stops the domain leaking into the protocol layer, which is what let the CWMP gateway grow a 1,200-line `main.go`. A rule nothing checks is a rule that erodes at the first convenient import.

**Files:**
- Create: `backend/internal/usp/boundary_test.go`

**Interfaces:**
- Consumes: nothing — it inspects the package graph.
- Produces: no exported symbols.

- [ ] **Step 1: Write the test**

Create `backend/internal/usp/boundary_test.go`:

```go
package usp_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestUSPImportsNoDomainPackages enforces design §4.1: internal/usp
// speaks protocol only. cmd/uspc is where protocol meets domain; if the
// codec could reach devices, jobs or store directly, that boundary would
// exist only in prose, and the first convenient import would erase it.
//
// This is a package-graph assertion rather than a grep, so it also
// catches a forbidden package reached transitively.
func TestUSPImportsNoDomainPackages(t *testing.T) {
	forbidden := []string{
		"acs/internal/devices",
		"acs/internal/jobs",
		"acs/internal/store",
		"acs/internal/sessions",
		"acs/internal/cwmp",
		"acs/cmd/",
	}

	for _, pkgPath := range []string{"acs/internal/usp", "acs/internal/usp/uspproto"} {
		pkg, err := build.Import(pkgPath, "", 0)
		if err != nil {
			t.Fatalf("import %s: %v", pkgPath, err)
		}
		// Imports of the package itself, plus its test files, since a
		// test that reached into the domain would defeat the point too.
		all := append([]string{}, pkg.Imports...)
		all = append(all, pkg.TestImports...)
		all = append(all, pkg.XTestImports...)

		for _, imported := range all {
			for _, bad := range forbidden {
				if imported == strings.TrimSuffix(bad, "/") || strings.HasPrefix(imported, bad) {
					t.Errorf("%s imports %s, which design §4.1 forbids: internal/usp speaks protocol only, and cmd/uspc is where protocol meets domain",
						pkgPath, imported)
				}
			}
		}
	}
}
```

Note the package clause is `usp_test`, not `usp` — this is an external test package, so its own imports do not pollute the package under test.

- [ ] **Step 2: Run the test to verify it passes on the current tree**

Run: `go test ./internal/usp/ -run TestUSPImportsNoDomainPackages -v`

Expected: PASS. Unlike the other tasks this test is green from the start — it is a guard, not a driver.

- [ ] **Step 3: Prove the guard can actually fail**

Temporarily add an import of a forbidden package to confirm the test is not vacuous. In `backend/internal/usp/record.go`, add `"acs/internal/devices"` to the import block and reference it with `var _ = devices.DataModelRootDevice2`, then:

Run: `go test ./internal/usp/ -run TestUSPImportsNoDomainPackages -v`

Expected: FAIL, naming `acs/internal/devices`.

**Then revert both edits** and re-run to confirm PASS again. Record both outputs in your report — a guard nobody has seen fail is a guard nobody should trust.

- [ ] **Step 4: Full backend checks**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean, and `git status --porcelain internal/usp/record.go` shows no modification — proving the Step 3 experiment was reverted.

- [ ] **Step 5: Commit**

```bash
git add internal/usp/boundary_test.go
git commit -F - <<'EOF'
test(usp): enforce that the protocol core imports no domain package

Design §4.1 requires internal/usp to speak protocol only, with cmd/uspc
as the place protocol meets domain. Asserted against the package graph
rather than by grep, so a forbidden package reached transitively is
caught too, and covering test imports as well since a test reaching into
the domain would defeat the point.

Verified the guard can fail by temporarily importing internal/devices
and watching it trip.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.** This plan implements the parts of the spec that are transport-free and domain-free:

| Spec section | Task |
|---|---|
| §3.1 schemas vendored, BSD-3-Clause, protobuf already in module graph | 1 |
| §3.2 Record structure, per-MTP connect records, PLAINTEXT | 1 (types), 3 (validation) |
| §3.3 message set, error codes 7000-7026 | 4 (messages), 5 (errors) |
| §3.3 `OperationComplete` carries `command_key` | 4 (`EncodeOperate` preserves it) |
| §3.4 accept `Record.version` 1.3, do not require 1.4 | 3 |
| §4.1 dependency rule: `internal/usp` imports no domain package | 6 |
| §5.3 Endpoint ID parsing as an optimisation, never an identity source | 2 |
| §6.4 error mapping with sentinels for 7013/7016/7006/7022 | 5 |
| §10 non-plaintext payload security rejected | 3 |

Deliberately **not** in this plan, and where each goes: MTP abstraction, WebSocket, MQTT, connection registry and `cmd/uspc` → B-2. `OnBoardRequest` reconciliation, the `usp_agents` table, job dispatch, `LISTEN/NOTIFY`, subscriptions and Notify routing → B-3. Session context and segmentation → out of scope entirely (§10). `Register`/`Deregister` → out of scope (§10), which is why Task 4 encodes no `Register`.

**2. Placeholder scan.** No `TBD`, `TODO`, "implement later", or "similar to Task N". Every code step carries the actual code. Task 6 Step 3 is a deliberate temporary edit with an explicit revert, not a placeholder.

**3. Type consistency.** `EndpointID` is defined in Task 2 and consumed by Task 3's `EncodeRecord`/`DecodeRecord`. `uspproto` types are produced by Task 1 and consumed by Tasks 3, 4 and 5 under the names listed in Task 1's Produces block. `ErrorCode` and `USPError` are defined in Task 5 only. `RecordVersion` is defined in Task 3 and is the value Task 3's own tests assert. `NewMsgID` is defined in Task 4 and used by no other task in this plan (B-2 and B-3 will call it).

**One risk carried deliberately.** Tasks 4 and 5 assume protoc-gen-go's casing for generated getters (`GetGetSupportedDm`, `GetUpdateObjs`, `GetParamSettings`, `GetControllerSupportedProtocolVersions`, `GetErrCode`). Task 4 Step 3 makes verifying those names an explicit step, and both tasks instruct the implementer to prefer the generated names and record any difference. The exported function signatures must not change, because B-2 and B-3 depend on them.

**Ordering.** Task 1 must be first — everything else imports its output. Task 2 is independent of 1. Tasks 3, 4 and 5 each depend only on 1 (and 3 on 2). Task 6 should be last so its guard sees the finished package.
