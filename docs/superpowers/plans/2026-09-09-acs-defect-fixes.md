# ACS Defect Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix four verified defects and two UAT blockers in the ACS platform, none of which depend on the dual-stack programme's later sub-projects.

**Architecture:** Six independent changes across three areas — the vendor parameter-resolution layer (`internal/devices/adapters`), the BSS webhook delivery worker (`cmd/bssadapter`), and the test/deploy harness (`cmd/acs` load test, `infra/docker-compose.yml`). No shared abstractions are introduced beyond one new resolver function; tasks can be reviewed and merged independently.

**Tech Stack:** Go 1.26+, PostgreSQL 18, Docker Compose. Standard library `crypto/hmac` and `testing`; no new dependencies.

**Spec:** No design doc — this is sub-project 0 of the programme recorded in [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) §2. Each defect below was verified during that design work against primary sources; the verification evidence is stated inline per task.

## Global Constraints

- Go module is `acs`; module Go directive is `go 1.26.6`. Do not raise it.
- No new third-party dependencies in any task. Standard library only.
- All backend checks must pass before each commit: `gofmt -l .` (must print nothing), `go vet ./...`, `go test ./...`.
- Run all commands from `backend/` unless a task says otherwise.
- Existing tests must not be weakened to accommodate a change. If a test's expectation was wrong, change the expectation and say so in the commit message.
- Commit messages: imperative mood, `type(scope): summary` — matching existing history (`fix(api): …`, `fix(web): …`, `feat: …`).

---

### Task 1: Correct the TR-181 cellular fallback paths

**Verification evidence:** The TR-181 Issue 2.19 data model
(`cwmp-data-models.broadband-forum.org/tr-181-2-19-0-cwmp.html`) profile index
lists `Device.Cellular.Interface.{i}.` as carrying `LastChange`,
`AvailableNetworks`, `RSSI`, `RSRP`, `RSRQ`, while
`Device.Cellular.Interface.{i}.Stats.` carries only byte/packet counters
(`BytesSent`, `BytesReceived`, `PacketsSent`, … , `Reset`). The string `SINR`
appears zero times in the entire model. All three currently-shipped fallback
paths are therefore wrong and can never return a value from a conformant device.

**Files:**
- Modify: `backend/internal/devices/adapters/vendor.go:44-54`
- Test: `backend/internal/devices/adapters/vendor_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `genericCellularFallback []string` (package-private, unchanged name and type). No exported surface changes.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/devices/adapters/vendor_test.go`:

```go
package adapters

import (
	"strings"
	"testing"
)

// TestGenericCellularFallbackUsesRealTR181Paths pins the fallback to paths
// that actually exist in TR-181 Device:2. RSSI/RSRP/RSRQ live directly on
// Device.Cellular.Interface.{i}; the Stats. sub-object holds only
// byte/packet counters, and SINR is not in the data model at all.
func TestGenericCellularFallbackUsesRealTR181Paths(t *testing.T) {
	want := []string{
		"Device.Cellular.Interface.1.RSRP",
		"Device.Cellular.Interface.1.RSRQ",
		"Device.Cellular.Interface.1.RSSI",
	}
	if len(genericCellularFallback) != len(want) {
		t.Fatalf("genericCellularFallback has %d entries, want %d: %v",
			len(genericCellularFallback), len(want), genericCellularFallback)
	}
	for i, w := range want {
		if genericCellularFallback[i] != w {
			t.Errorf("genericCellularFallback[%d] = %q, want %q", i, genericCellularFallback[i], w)
		}
	}
}

// TestGenericCellularFallbackAvoidsStatsAndSINR guards the two specific
// mistakes that were shipped: signal metrics under .Stats., and SINR, which
// TR-181 does not define.
func TestGenericCellularFallbackAvoidsStatsAndSINR(t *testing.T) {
	for _, p := range genericCellularFallback {
		if strings.Contains(p, ".Stats.") {
			t.Errorf("%q puts a signal metric under .Stats., which holds only packet counters", p)
		}
		if strings.Contains(strings.ToUpper(p), "SINR") {
			t.Errorf("%q uses SINR, which is not defined anywhere in TR-181", p)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/devices/adapters/ -run TestGenericCellularFallback -v`

Expected: both tests FAIL. The first reports
`genericCellularFallback[0] = "Device.Cellular.Interface.1.Stats.RSRP", want "Device.Cellular.Interface.1.RSRP"`;
the second reports `.Stats.` and `SINR` violations.

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/devices/adapters/vendor.go`, replace the
`genericCellularFallback` declaration and its doc comment with:

```go
// genericCellularFallback is the standard TR-181 Cellular signal-quality
// path set used when a device's manufacturer doesn't match any known
// vendor catalog.
//
// These sit directly on Device.Cellular.Interface.{i} — verified against
// TR-181 Issue 2.19, whose profile index lists RSSI/RSRP/RSRQ on the
// Interface object and confines Stats. to byte/packet counters. There is
// deliberately no SINR entry: TR-181 does not define one at all, so any
// device reporting SINR does so through a vendor extension, which is the
// per-vendor catalog's job rather than this fallback's.
var genericCellularFallback = []string{
	"Device.Cellular.Interface.1.RSRP",
	"Device.Cellular.Interface.1.RSRQ",
	"Device.Cellular.Interface.1.RSSI",
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devices/adapters/ -v`

Expected: PASS, including the pre-existing `canonical_test.go` and
`catalog_test.go` tests.

- [ ] **Step 5: Verify the whole backend still passes**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/devices/adapters/vendor.go internal/devices/adapters/vendor_test.go
git commit -m "fix(adapters): correct the TR-181 cellular fallback paths

RSSI/RSRP/RSRQ sit directly on Device.Cellular.Interface.{i}; the Stats.
sub-object holds only byte/packet counters, so all three shipped fallback
paths could never resolve on a conformant device. SINR is dropped
entirely -- TR-181 does not define it, so it belongs in a per-vendor
catalog, not the generic fallback. Verified against TR-181 Issue 2.19."
```

---

### Task 2: Add ordered path candidates to the canonical resolver

**Why:** TR-098 defines *two* valid locations for a WiFi passphrase —
`WLANConfiguration.{i}.KeyPassphrase` and
`WLANConfiguration.{i}.PreSharedKey.{i}.KeyPassphrase` — both confirmed present
in the TR-098 Issue 1.8 model. A single-path resolver cannot express that. This
task adds the candidate list; Task 3 changes which one is preferred.

**Files:**
- Modify: `backend/internal/devices/adapters/canonical.go`
- Test: `backend/internal/devices/adapters/canonical_test.go`

**Interfaces:**
- Consumes: existing `CanonicalParameter` constants, `device2Paths`, `igd1Paths`, and `ResolvePath` from `canonical.go`.
- Produces: `func ResolvePathCandidates(root string, p CanonicalParameter) []string` — returns candidate paths in preference order, most-preferred first; returns `nil` when the parameter is unknown for that root. `ResolvePath` keeps its existing signature `(string, bool)` and returns the first candidate.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/devices/adapters/canonical_test.go`:

```go
func TestResolvePathCandidatesSinglePathParameters(t *testing.T) {
	got := ResolvePathCandidates(devices.DataModelRootDevice2, DeviceInfoSoftwareVersion)
	want := []string{"Device.DeviceInfo.SoftwareVersion"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ResolvePathCandidates(DEVICE2, software_version) = %v, want %v", got, want)
	}
}

func TestResolvePathCandidatesUnknownParameterReturnsNil(t *testing.T) {
	if got := ResolvePathCandidates(devices.DataModelRootDevice2, CanonicalParameter("nope.not.real")); got != nil {
		t.Errorf("ResolvePathCandidates for an unknown parameter = %v, want nil", got)
	}
}

// ResolvePath must stay consistent with ResolvePathCandidates: it returns
// the single most-preferred candidate.
func TestResolvePathReturnsFirstCandidate(t *testing.T) {
	for _, root := range []string{devices.DataModelRootDevice2, devices.DataModelRootIGD1} {
		for _, p := range []CanonicalParameter{DeviceInfoSoftwareVersion, WiFiSSID, WiFiKeyPassphrase} {
			cands := ResolvePathCandidates(root, p)
			path, ok := ResolvePath(root, p)
			if len(cands) == 0 {
				t.Fatalf("root %q param %q: no candidates", root, p)
			}
			if !ok || path != cands[0] {
				t.Errorf("root %q param %q: ResolvePath = (%q, %v), want (%q, true)", root, p, path, ok, cands[0])
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/devices/adapters/ -run TestResolvePathCandidates -v`

Expected: compile FAIL — `undefined: ResolvePathCandidates`.

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/devices/adapters/canonical.go`, add after the existing
`ResolvePath` function:

```go
// igd1PathAlternates lists additional, less-preferred TR-098 locations for
// a canonical parameter, appended after the igd1Paths entry.
//
// TR-098 Issue 1.8 defines both WLANConfiguration.{i}.KeyPassphrase and
// WLANConfiguration.{i}.PreSharedKey.{i}.KeyPassphrase, and real devices
// disagree about which one is writable, so a single path cannot express
// the truth. Preference order is set in igd1Paths; this holds the rest.
var igd1PathAlternates = map[CanonicalParameter][]string{}

// ResolvePathCandidates returns every known path for a canonical parameter
// under the given data-model root, most-preferred first, or nil when the
// parameter is unknown for that root.
//
// Callers that know which paths a device actually advertises (and whether
// they are writable) should pick the first candidate the device supports.
// Callers without that information should use the first entry, which is
// what ResolvePath returns.
func ResolvePathCandidates(root string, p CanonicalParameter) []string {
	table, alternates := device2Paths, map[CanonicalParameter][]string(nil)
	if root == DataModelRootIGD1Value {
		table, alternates = igd1Paths, igd1PathAlternates
	}
	primary, ok := table[p]
	if !ok {
		return nil
	}
	out := make([]string, 0, 1+len(alternates[p]))
	out = append(out, primary)
	out = append(out, alternates[p]...)
	return out
}
```

Then, still in `canonical.go`, add this constant near the top of the file so the
package does not need to import `internal/devices` (which would create an import
cycle — `devices` already imports nothing from `adapters`, but keeping
`adapters` free of the dependency preserves that):

```go
// DataModelRootIGD1Value mirrors devices.DataModelRootIGD1 without taking a
// dependency on that package.
const DataModelRootIGD1Value = "IGD1"
```

Finally, rewrite `ResolvePath`'s body to delegate, keeping its signature and
its existing TR-181-first fallback behaviour:

```go
func ResolvePath(root string, p CanonicalParameter) (string, bool) {
	cands := ResolvePathCandidates(root, p)
	if len(cands) == 0 {
		return "", false
	}
	return cands[0], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devices/adapters/ -v`

Expected: PASS. All pre-existing `canonical_test.go` tests must still pass
unchanged — this task is behaviour-preserving.

- [ ] **Step 5: Verify the whole backend still passes**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/devices/adapters/canonical.go internal/devices/adapters/canonical_test.go
git commit -m "refactor(adapters): resolve canonical parameters to ordered candidates

TR-098 defines two valid WiFi passphrase locations and real devices
disagree about which is writable, which a single-path resolver cannot
express. ResolvePathCandidates returns every known path most-preferred
first; ResolvePath keeps its signature and returns the first. Behaviour
is unchanged -- this only adds the seam the next commit needs."
```

---

### Task 3: Prefer the PreSharedKey passphrase path on TR-098

**Verification evidence:** Both paths exist in the TR-098 Issue 1.8 model
(anchors `…WLANConfiguration.KeyPassphrase` and
`…WLANConfiguration.PreSharedKey.KeyPassphrase`). Field reports across Huawei
EchoLife ONTs (HG8546M, HG8145V5, HG8245H, EG8141A5) consistently show
`KeyPassphrase` advertised with `writable=false` and writes failing — sometimes
with CWMP fault 9007 — while `PreSharedKey.1.KeyPassphrase` accepts a plaintext
write. Huawei is the largest ONT installed base, so the current default fails on
the most common TR-098 fleet.

**Known risk, accepted:** a TR-098 device that implements *only*
`KeyPassphrase` will now receive the `PreSharedKey.1.KeyPassphrase` path first.
This plan does not add writability-aware selection, because the BSS adapter's
`Translate` has no access to a device's discovered parameter names. That
selection belongs to the compatibility-layer work in sub-project A/B, which is
why Task 2 built the candidate list rather than swapping one string. Record this
in `docs/COMPATIBILITY.md` so the trade-off is visible.

**Files:**
- Modify: `backend/internal/devices/adapters/canonical.go` (the `igd1Paths` and `igd1PathAlternates` entries for `WiFiKeyPassphrase`)
- Modify: `docs/COMPATIBILITY.md` (add a Huawei deviation note)
- Test: `backend/internal/devices/adapters/canonical_test.go`

**Interfaces:**
- Consumes: `ResolvePathCandidates(root string, p CanonicalParameter) []string` from Task 2.
- Produces: no new symbols. Changes the value returned by `ResolvePath(IGD1, WiFiKeyPassphrase)`.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/devices/adapters/canonical_test.go`:

```go
// TestIGD1PassphrasePrefersPreSharedKey pins the preference order. Huawei
// EchoLife ONTs advertise WLANConfiguration.{i}.KeyPassphrase as
// non-writable and only accept PreSharedKey.1.KeyPassphrase, and they are
// the largest TR-098 fleet, so the PreSharedKey form leads.
func TestIGD1PassphrasePrefersPreSharedKey(t *testing.T) {
	got := ResolvePathCandidates(devices.DataModelRootIGD1, WiFiKeyPassphrase)
	want := []string{
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase",
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase",
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

Then update the existing `TestResolvePathIGD1` expectation in the same file —
its `WiFiKeyPassphrase` entry currently asserts the bare `KeyPassphrase` path.
Change that one map entry to:

```go
		WiFiKeyPassphrase:                     "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase",
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/devices/adapters/ -run 'TestIGD1Passphrase|TestResolvePathIGD1' -v`

Expected: both FAIL. `TestIGD1PassphrasePrefersPreSharedKey` reports one
candidate instead of two; `TestResolvePathIGD1` reports the bare `KeyPassphrase`
path where the `PreSharedKey.1` form is now wanted.

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/devices/adapters/canonical.go`, change the
`WiFiKeyPassphrase` entry in `igd1Paths` to the PreSharedKey form:

```go
	WiFiKeyPassphrase:                     "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase",
```

and populate `igd1PathAlternates` (added empty in Task 2) with the older form as
the fallback candidate:

```go
// igd1PathAlternates lists additional, less-preferred TR-098 locations for
// a canonical parameter, appended after the igd1Paths entry.
//
// TR-098 Issue 1.8 defines both WLANConfiguration.{i}.KeyPassphrase and
// WLANConfiguration.{i}.PreSharedKey.{i}.KeyPassphrase, and real devices
// disagree about which one is writable, so a single path cannot express
// the truth. Huawei EchoLife ONTs -- the largest TR-098 fleet -- advertise
// the bare KeyPassphrase as non-writable and only accept the PreSharedKey
// form, so that leads; the bare form stays as a candidate for the simpler
// devices that implement only it.
var igd1PathAlternates = map[CanonicalParameter][]string{
	WiFiKeyPassphrase: {
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase",
	},
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devices/adapters/ ./internal/bss/ -v`

Expected: PASS. `internal/bss` is included because `translateModifyWifi` calls
`ResolvePath` for this parameter; if `internal/bss/template_test.go` asserts the
old path, update that expectation too and note it in the commit message.

- [ ] **Step 5: Document the deviation**

In `docs/COMPATIBILITY.md`, under the "Vendor profiles" table's Huawei row
`Notes` cell, append:

```
Bare `WLANConfiguration.{i}.KeyPassphrase` is advertised but non-writable on EchoLife ONTs (HG8546M, HG8145V5, HG8245H, EG8141A5); writes must target `PreSharedKey.1.KeyPassphrase`, which is now the preferred TR-098 candidate. A TR-098 device implementing only the bare form is a known unqualified case until writability-aware candidate selection lands.
```

- [ ] **Step 6: Verify the whole backend still passes**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all packages `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/devices/adapters/canonical.go internal/devices/adapters/canonical_test.go ../docs/COMPATIBILITY.md
git commit -m "fix(adapters): write TR-098 WiFi passphrases to PreSharedKey.1

Huawei EchoLife ONTs advertise WLANConfiguration.{i}.KeyPassphrase but
report it non-writable, so every passphrase write to the largest TR-098
fleet resolved, sent and silently failed. Both paths are valid TR-098, so
the PreSharedKey form now leads and the bare form remains a candidate.

Devices implementing only the bare form are recorded in COMPATIBILITY.md
as unqualified until writability-aware selection lands with the
compatibility-layer work."
```

---

### Task 4: Sign webhooks over id, timestamp and payload

**Verification evidence:** `cmd/bssadapter/webhook_worker.go:178-180` computes
`hmac.New(sha256.New, secret)` over `d.Payload` alone, and sets only
`Content-Type`, `X-Webhook-Signature` and `X-Webhook-Event`. No timestamp or
delivery id is sent in a header, and the payload struct
(`webhook_worker.go:41-49`) carries neither. A captured delivery is therefore
replayable indefinitely, and a consumer has nothing to dedupe or age-check
against. `external_order_id` cannot serve as the dedupe key because legitimate
retries of the same order share it. The
[Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)
spec signs `msg_id.timestamp.payload` precisely to close this.

**Files:**
- Modify: `backend/cmd/bssadapter/webhook_worker.go:163-190`
- Modify: `bss-integration-guide.md` (document the new headers)
- Test: `backend/cmd/bssadapter/webhook_signature_test.go` (create — this package currently has no tests)

**Interfaces:**
- Consumes: `bss.WebhookDelivery` (fields `ID string`, `Payload json.RawMessage`, `Secret string`, `EventType string` — already present).
- Produces: `func webhookSignature(secret, msgID, timestamp string, payload []byte) string` — returns the lowercase hex HMAC-SHA256 of `msgID + "." + timestamp + "." + payload`. Package-private, in `webhook_worker.go`.

- [ ] **Step 1: Write the failing test**

Create `backend/cmd/bssadapter/webhook_signature_test.go`:

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestWebhookSignatureCoversIDAndTimestamp is the whole point of the
// change: a body-only HMAC is replayable forever, because the consumer has
// nothing to age-check or dedupe against. The signed string must bind the
// delivery id and the send time to the payload.
func TestWebhookSignatureCoversIDAndTimestamp(t *testing.T) {
	const (
		secret  = "whsec_test_secret_value_at_least_24b"
		msgID   = "3f7c1b9e-0000-4000-8000-000000000001"
		tsOne   = "1788950000"
		tsTwo   = "1788950001"
		payload = `{"event_type":"order.completed"}`
	)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID + "." + tsOne + "." + payload))
	want := hex.EncodeToString(mac.Sum(nil))

	got := webhookSignature(secret, msgID, tsOne, []byte(payload))
	if got != want {
		t.Errorf("webhookSignature = %q, want %q", got, want)
	}

	// A body-only HMAC must NOT match — that is the bug being fixed.
	bodyOnly := hmac.New(sha256.New, []byte(secret))
	bodyOnly.Write([]byte(payload))
	if got == hex.EncodeToString(bodyOnly.Sum(nil)) {
		t.Error("signature equals a body-only HMAC; id and timestamp are not bound in")
	}

	// Changing only the timestamp must change the signature, or replay
	// protection is decorative.
	if webhookSignature(secret, msgID, tsTwo, []byte(payload)) == got {
		t.Error("signature is unchanged when the timestamp changes")
	}

	// Changing only the delivery id must change the signature.
	if webhookSignature(secret, "3f7c1b9e-0000-4000-8000-000000000002", tsOne, []byte(payload)) == got {
		t.Error("signature is unchanged when the delivery id changes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/bssadapter/ -run TestWebhookSignature -v`

Expected: compile FAIL — `undefined: webhookSignature`.

- [ ] **Step 3: Write minimal implementation**

In `backend/cmd/bssadapter/webhook_worker.go`, add the helper:

```go
// webhookSignature signs a delivery the way Standard Webhooks specifies:
// HMAC-SHA256 over "<msg-id>.<timestamp>.<payload>", hex-encoded.
//
// Binding the id and timestamp into the signed string is what makes the
// signature non-replayable. Signing the body alone -- which this worker
// used to do -- produces a token an interceptor can resend forever, with
// the consumer unable to tell the difference.
func webhookSignature(secret, msgID, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/bssadapter/ -run TestWebhookSignature -v`

Expected: PASS.

- [ ] **Step 5: Use the helper and send the new headers**

In `sendWebhookDelivery`, replace the three signing lines
(`mac := hmac.New(...)` through `signature := hex.EncodeToString(mac.Sum(nil))`)
with:

```go
	// Fresh timestamp per attempt, not per delivery: a retry an hour later
	// must still land inside the consumer's freshness window, while any
	// captured copy of an earlier attempt falls outside it.
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := webhookSignature(d.Secret, d.ID, timestamp, d.Payload)
```

and replace the header block with:

```go
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Webhook-Id", d.ID)
	req.Header.Set("Webhook-Timestamp", timestamp)
	req.Header.Set("Webhook-Signature", "v1,"+signature)
	req.Header.Set("X-Webhook-Event", d.EventType)
	// Retained for consumers written against the previous contract. It
	// carries the same value as Webhook-Signature's v1 scheme but without
	// the scheme prefix; new consumers should verify Webhook-Signature.
	req.Header.Set("X-Webhook-Signature", signature)
```

Update the `sendWebhookDelivery` doc comment to describe the signed string
rather than the old body-only scheme. Add `"strconv"` to the import block if it
is not already present (`time` already is).

- [ ] **Step 6: Run the full backend checks**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all packages `ok`.

- [ ] **Step 7: Document the contract**

In `bss-integration-guide.md`, find the webhook section and add:

```markdown
Each delivery carries:

| Header | Value |
|---|---|
| `Webhook-Id` | Unique delivery id. Stable across retries of the same delivery — use it as your idempotency key. |
| `Webhook-Timestamp` | Unix seconds at send time. Fresh per attempt. |
| `Webhook-Signature` | `v1,<hex>` where `<hex>` is HMAC-SHA256 over `<Webhook-Id>.<Webhook-Timestamp>.<raw body>` using your subscription secret. |
| `X-Webhook-Event` | Event type. |
| `X-Webhook-Signature` | Deprecated. Same hex as the `v1` scheme above, without the prefix. |

Verify by recomputing the HMAC over the concatenation — not over the body
alone — and reject deliveries whose `Webhook-Timestamp` is outside a
tolerance window (5 minutes is the common choice). Deduplicate on
`Webhook-Id`: delivery is at-least-once, so the same id can arrive twice.
```

- [ ] **Step 8: Commit**

```bash
git add cmd/bssadapter/webhook_worker.go cmd/bssadapter/webhook_signature_test.go ../bss-integration-guide.md
git commit -m "fix(bss): make webhook signatures non-replayable

The HMAC covered the body alone, and no timestamp or delivery id was
sent, so a captured delivery could be replayed indefinitely with the
consumer unable to detect it. external_order_id cannot serve as the
dedupe key either, since legitimate retries share it.

Signatures now cover \"<id>.<timestamp>.<body>\" per Standard Webhooks,
with Webhook-Id, Webhook-Timestamp and Webhook-Signature headers. The old
X-Webhook-Signature header is retained, marked deprecated, for consumers
written against the previous contract."
```

---

### Task 5: Make the compose quick-start work without Grafana secrets

**Verification evidence:** `README.md` instructs
`docker compose -f infra/docker-compose.yml up -d postgres`. Run verbatim, it
aborts before starting anything:

```
error while interpolating services.grafana.environment.ACS_GRAFANA_DB_PASSWORD: required variable ACS_GRAFANA_DB_PASSWORD is missing a value
error while interpolating services.grafana.environment.GF_SECURITY_ADMIN_PASSWORD: required variable GRAFANA_ADMIN_PASSWORD is missing a value
```

Compose interpolates the whole file before selecting services, so the `:?`
mandatory-variable operator on the Grafana service blocks an unrelated one. This
is the first command anyone runs when standing up a UAT environment.

**Files:**
- Modify: `infra/docker-compose.yml:78` and `infra/docker-compose.yml:84`
- Test: manual verification (no Go test — this is compose configuration)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: no code symbols. `docker compose … up -d postgres` succeeds with no Grafana variables set; Grafana itself still refuses to start with a placeholder password.

- [ ] **Step 1: Reproduce the failure**

Run from the repository root:

```bash
env -u GRAFANA_ADMIN_PASSWORD -u ACS_GRAFANA_DB_PASSWORD \
  docker compose -f infra/docker-compose.yml config >/dev/null
```

Expected: FAIL with the two `required variable … is missing a value` errors above.

- [ ] **Step 2: Change the mandatory operators to defaults**

In `infra/docker-compose.yml`, change line 78 from
`${GRAFANA_ADMIN_PASSWORD:?set GRAFANA_ADMIN_PASSWORD}` to:

```yaml
      GF_SECURITY_ADMIN_PASSWORD: ${GRAFANA_ADMIN_PASSWORD:-change-me}
```

and line 84 from `${ACS_GRAFANA_DB_PASSWORD:?set ACS_GRAFANA_DB_PASSWORD}` to:

```yaml
      ACS_GRAFANA_DB_PASSWORD: ${ACS_GRAFANA_DB_PASSWORD:-change-me}
```

This is safe because `change-me` is the placeholder the platform already
fails closed on: services reject placeholder secrets at startup, and CI has a
job asserting that. Selecting `postgres` no longer requires another service's
secrets, while starting Grafana with an unset password still fails loudly —
it just fails in Grafana's own startup rather than in file interpolation.

- [ ] **Step 3: Verify interpolation now succeeds**

Run from the repository root:

```bash
env -u GRAFANA_ADMIN_PASSWORD -u ACS_GRAFANA_DB_PASSWORD \
  docker compose -f infra/docker-compose.yml config >/dev/null && echo "CONFIG OK"
```

Expected: prints `CONFIG OK`.

- [ ] **Step 4: Verify the documented quick start actually runs**

Run from the repository root:

```bash
env -u GRAFANA_ADMIN_PASSWORD -u ACS_GRAFANA_DB_PASSWORD \
  docker compose -f infra/docker-compose.yml up -d postgres
docker compose -f infra/docker-compose.yml ps postgres
```

Expected: the container starts and reports healthy. Then stop it:
`docker compose -f infra/docker-compose.yml down`.

- [ ] **Step 5: Commit**

```bash
git add infra/docker-compose.yml
git commit -m "fix(infra): unbreak the documented compose quick start

Compose interpolates the whole file before selecting services, so the
mandatory-variable operators on Grafana aborted the README's
\"up -d postgres\" command before anything started -- the first command
anyone runs when standing up an environment.

Both now default to the change-me placeholder the platform already fails
closed on, so Grafana still refuses to start unconfigured; it just fails
in its own startup instead of blocking an unrelated service."
```

---

### Task 6: Make the load harness count connection failures

**Verification evidence:** `cmd/acs/session_integration_test.go:69` calls
`c.t.Fatal(err)` inside `mockCPE.post`, which the load test invokes from worker
goroutines. `t.Fatal` from a non-test goroutine runs `runtime.Goexit()`, so the
goroutine terminates before reaching `errs <- …` — while `defer wg.Done()` still
runs. The failure counter therefore stays at zero. Observed at
`ACS_TEST_LOAD_DEVICES=500`: `load: 500 devices, 0 failed, 1358.1 sessions/s,
317 registered` — 183 devices lost, reported as zero failures. Any load evidence
this harness produces currently overstates success.

**Files:**
- Modify: `backend/cmd/acs/session_integration_test.go:55-70` (add an
  error-returning variant) and `:570-604` (use it, count failures)
- Test: the load test is itself the test; a new unit test covers the counting.

**Interfaces:**
- Consumes: existing `mockCPE` struct and its `post(body string) (int, string)` method.
- Produces: `func (c *mockCPE) tryPost(body string) (int, string, error)` — performs the request and returns any transport error instead of aborting the goroutine. `post` keeps its signature and calls `t.Fatal` on error, so every existing caller is unaffected.

- [ ] **Step 1: Write the failing test**

Append to `backend/cmd/acs/session_integration_test.go`:

```go
// TestTryPostReturnsTransportErrors is a regression guard for the load
// harness's failure counting. post() calls t.Fatal, which from a worker
// goroutine runs runtime.Goexit() and kills the goroutine before it can
// record the failure -- which is why a run that lost 183 of 500 devices
// still reported "0 failed". tryPost must hand the error back instead.
func TestTryPostReturnsTransportErrors(t *testing.T) {
	// A closed listener's address: nothing is accepting connections.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/cwmp"
	srv.Close()

	cpe := &mockCPE{t: t, client: &http.Client{Timeout: 2 * time.Second}, url: url}
	code, _, err := cpe.tryPost("")
	if err == nil {
		t.Fatalf("tryPost against a closed server returned err = nil (code %d), want a transport error", code)
	}
	if code != 0 {
		t.Errorf("tryPost returned code %d alongside an error, want 0", code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/acs/ -run TestTryPostReturnsTransportErrors -v`

Expected: compile FAIL — `cpe.tryPost undefined (type *mockCPE has no field or method tryPost)`.

- [ ] **Step 3: Split post into an error-returning variant**

In `backend/cmd/acs/session_integration_test.go`, rename the existing
`post` method to `tryPost`, change its signature to
`func (c *mockCPE) tryPost(body string) (int, string, error)`, replace the
`c.t.Fatal(err)` branch with `return 0, "", err`, and make every existing
`return` in that method return a nil error as its third value. Then add a `post`
wrapper preserving the old contract:

```go
// post performs a CWMP POST and aborts the test on a transport error.
// Safe only from the goroutine running the test: t.Fatal elsewhere runs
// runtime.Goexit() and silently kills the caller. Concurrent callers must
// use tryPost and handle the error themselves.
func (c *mockCPE) post(body string) (int, string) {
	c.t.Helper()
	code, body2, err := c.tryPost(body)
	if err != nil {
		c.t.Fatal(err)
	}
	return code, body2
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/acs/ -run TestTryPostReturnsTransportErrors -v`

Expected: PASS.

- [ ] **Step 5: Count failures in the load test**

In `TestIntegration_CPELoad`, replace the goroutine body so both posts use
`tryPost` and every failure reaches the channel:

```go
		go func(i int) {
			defer wg.Done()
			cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
			serial := fmt.Sprintf("LOAD%06d", i)
			code, _, err := cpe.tryPost(vendorInform(t, "Zyxel", "001349", "NR7101", serial))
			if err != nil {
				errs <- fmt.Errorf("device %d Inform transport error: %w", i, err)
				return
			}
			if code != 200 {
				errs <- fmt.Errorf("device %d Inform: %d", i, code)
				return
			}
			if _, _, err := cpe.tryPost(""); err != nil {
				errs <- fmt.Errorf("device %d session close transport error: %w", i, err)
			}
		}(i)
```

- [ ] **Step 6: Verify the count is now honest**

Run, from `backend/`, with a Postgres available (see the DSN in
`docs/COMPATIBILITY.md` §Load evidence):

```bash
ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs_load?sslmode=disable" \
ACS_TEST_LOAD_DEVICES=500 \
go test -count=1 -p 1 -run TestIntegration_CPELoad -v ./cmd/acs/
```

Expected: the `load:` line's `%d failed` figure is now non-zero and
approximately `n - registered`, instead of `0`. The test still fails at 500 on a
constrained host — that is correct behaviour; the point of this task is that the
number is truthful.

- [ ] **Step 7: Run the full backend checks**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all packages `ok`
(DB-backed tests skip without a DSN).

- [ ] **Step 8: Commit**

```bash
git add cmd/acs/session_integration_test.go
git commit -m "fix(test): count transport failures in the CPE load harness

mockCPE.post calls t.Fatal, which from a worker goroutine runs
runtime.Goexit() and kills the goroutine before it records the failure,
while the WaitGroup defer still fires. A 500-device run that lost 183
devices therefore reported \"0 failed\", so any load evidence the harness
produced overstated success.

tryPost returns the transport error instead; post keeps its signature for
the sequential callers. The load test now attributes every lost device."
```

---

## Self-Review

**1. Spec coverage.** This plan implements sub-project 0 as scoped in the USP
design's §2 programme table, plus UAT blockers B1 and B4 from the readiness
assessment:

| Item | Task |
|---|---|
| Cellular fallback paths wrong | 1 |
| TR-098 passphrase path fails on Huawei | 2 (seam), 3 (fix) |
| Webhook signature replayable | 4 |
| UAT blocker B1 — compose quick start aborts | 5 |
| UAT blocker B4 — load harness under-reports failures | 6 |

Deliberately **not** in this plan, and why: UAT blocker B2 (real-device
qualification) needs physical hardware, not code; B3 (TLS on CWMP) and B5 (UAT
environment) are deployment configuration requiring administrator action; the
four unsourced vendor catalogs need a `GetParameterNames` dump from real
hardware. Writability-aware candidate selection is explicitly deferred to
sub-project A/B and recorded as a known gap in Task 3.

**2. Placeholder scan.** No `TBD`, `TODO`, "implement later", "add appropriate
error handling", or "similar to Task N". Every code step carries the actual code.
Task 5 has no Go test because it changes a compose file; its verification is two
concrete commands with expected output.

**3. Type consistency.** `ResolvePathCandidates(root string, p CanonicalParameter) []string`
is defined in Task 2 and consumed with that exact signature in Task 3.
`webhookSignature(secret, msgID, timestamp string, payload []byte) string` is
defined and consumed within Task 4. `tryPost(body string) (int, string, error)`
is defined in Task 6 Step 3 and consumed in Step 5. `DataModelRootIGD1Value`
is introduced in Task 2 and used only there. `igd1PathAlternates` is declared
empty in Task 2 and populated in Task 3 — Task 3 restates the full declaration
so it can be applied without reading Task 2.

**Cross-task ordering note.** Tasks 2 and 3 must run in order; 1, 4, 5 and 6 are
independent of everything else and of each other.
