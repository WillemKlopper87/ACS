# Nokia FastMile 5G → ACS on EC2: lab runbook

Checked against ACS `main` @ `8ed5d7f` (2026-10-20) on 2026-10-30.
Based on the repo's own `docs/NOKIA-CLOUD-QUALIFICATION.md` (issue #75), boiled down to the steps you actually do.

**Script readiness:** `scripts/quickstart.sh` is a complete one-shot installer for a fresh Ubuntu 22.04/24.04 box. It installs Go 1.26.6 (matches `backend/go.mod`), Node 22 and Docker, clones `main`, generates secrets once into `~/.acs-secrets.env`, runs DB migrations plus a migration check, starts every service and gates on a whole-stack health check. All scripts pass `bash -n`. The backend builds, `go vet` is clean and all 44 Go test packages pass. Frontend lint, build and 65 tests pass, and `npm audit` reports 0 vulnerabilities. It has not yet been run on a real EC2 host from this machine; step 3 is that run.

---

## 0. Before you touch anything: write down the Nokia's current TR-069 settings

If the FastMile is currently managed by a carrier ACS, pointing it at yours **cuts the carrier off** until you change it back. On the Nokia web UI (`http://192.168.1.1`), open the TR-069 / CWMP / Management Server page and record the following:

| Field | Original value |
|---|---|
| ACS URL | |
| ACS Username (often `hgw`) | |
| ACS Password | |
| Connection Request Username (often `itms`) | |
| Connection Request Password | |
| Periodic Inform enabled / interval | |

Take a screenshot as well. You need these to restore the CPE afterwards.

---

### Your FastMile as of 2026-10-30 (from the WAN / TR-069 and WAN / TR-369 pages)

| WAN / TR-069 field | Current | Set for the lab test |
|---|---|---|
| Periodic inform enable | on | **on** |
| Periodic inform interval(s) | `43200` (12 h) | **`60`** |
| URL | `http://0.0.0.0` (no ACS configured) | **`http://<EC2_PUBLIC_IP>:7547/cwmp`** |
| Username | `hgw` | **`acs-bootstrap`** |
| Password | (hidden) | **`ACS_CWMP_BOOTSTRAP_PASSWORD`** from `show-cpe-setup.sh --show-secrets` |
| Connection request URL | `http://10.10.178.14:7547/` | **leave unchanged** (device-generated) |
| Connection request username | `itms` | **`acs-connreq`** |
| Connection request password | (hidden) | **`ACS_CONNECTION_REQUEST_PASSWORD`** |

What these values tell us:

- **The URL `http://0.0.0.0` means no carrier ACS is managing this CPE over CWMP.** Pointing it at your EC2 box doesn't cut anyone off. To restore it later, set the URL back to `http://0.0.0.0` and the interval back to `43200`.
- **The CR URL `10.10.178.14` is a private carrier address, which confirms CGNAT.** The cloud ACS can't reach the Nokia directly, so every job waits for the next periodic Inform. That's why the interval drops to 60 s for the test.
- **Leave the WAN / TR-369 page alone.** USP is enabled toward Nokia's own cloud controller (`self.nokia:nwcc`, MQTT over TLS, port 443) with an empty broker address. This test uses only TR-069. The lab ACS's USP MQTT is plaintext on 1883, so it can't serve this TLS-only USP agent without more work.
- **Device identity (Root details page):** FastMile 5G Gateway 2, serial `CSC25430018F`, hardware `3TG02241AAAA`, software `1.2501.00.0798`. These match `ci/compatibility/nokia-read-only.json` exactly, so the identity gate in step 6 should pass. The first Inform must show the same serial; check it in `logs.sh`.
- Memory usage was **83%** at 2 days' uptime. If the TR-069 client misbehaves (sessions hang or don't restart), reboot the Nokia once before blaming the ACS.
- The `hgw` password is hidden and you don't need it back. Once the ACS graduates the device, it replaces that password with its own unique one anyway.

---

## 1. Launch the EC2 instance (AWS console, manual)

Region **af-south-1 (Cape Town)**. EC2 → Launch instance:

| Setting | Value |
|---|---|
| Name | `ACS-Nokia-Lab` |
| AMI | **Ubuntu Server 24.04 LTS** (x86_64) |
| Instance type | **t3.medium** (4 GB RAM). Don't use t3.small: the Go and frontend builds can run out of memory on 2 GB. |
| Key pair | create or choose one, and keep the `.pem` |
| Storage | **30 GB gp3** |
| Auto-assign public IP | **Enable**. Elastic IP is optional; without one the IP changes on stop/start. |

### Security group: inbound rules

Create a new SG `acs-nokia-lab`:

| Port | Proto | Source | Why |
|---|---|---|---|
| 22 | TCP | **My IP** | SSH |
| 5173 | TCP | **My IP** | ACS web console (lab) |
| 8080 | TCP | **My IP** | ACS REST API (the console calls it from your browser) |
| **7547** | TCP | `0.0.0.0/0` **for first contact only** (see note) | CWMP: Nokia → ACS |
| 3478 | UDP | *leave closed for now* | STUN, only needed later for Connection Request testing |

> **Why 7547 has to be open to everyone at first:** a 5G FastMile reaches the internet through the mobile carrier's CGNAT, so its source IP is a carrier pool address you can't know in advance. Open 7547 to `0.0.0.0/0`, wait for the first Inform, read the Nokia's real source IP from the logs (step 5), then **tighten the rule to that /24 or the carrier's range**. Bootstrap auth and rate limits protect the port in the meantime.

Do **not** open 5432, 8090, 8092, 3000 or 9090. They stay on localhost.

---

## 2. SSH in

From PowerShell on your PC:

```bash
ssh -i C:\path\to\your-key.pem ubuntu@<EC2_PUBLIC_IP>
```

---

## 3. Clone the private repo, then run the installer

> The `WillemKlopper87/ACS` repo is **private**, so the `curl raw.githubusercontent.com/... | bash` one-liner in the EC2 guide returns **404** on a fresh box. Clone with a read-only **deploy key** first, then run `quickstart.sh` from the clone.

On the EC2 box, as the `ubuntu` user (**not** root or sudo; the script refuses root):

**3a. Create a deploy key on the instance:**

```bash
ssh-keygen -t ed25519 -f ~/.ssh/acs_deploy_key -N "" -C "acs-nokia-lab"
cat ~/.ssh/acs_deploy_key.pub
```

**3b. Add it to GitHub:** open github.com/WillemKlopper87/ACS → **Settings → Deploy keys → Add deploy key**. Title it `acs-nokia-lab`, paste the `.pub` line, and **leave "Allow write access" unchecked**. Save.

**3c. Point SSH at the key and clone:**

```bash
cat >> ~/.ssh/config <<'EOF'
Host github.com
  IdentityFile ~/.ssh/acs_deploy_key
  IdentitiesOnly yes
EOF
chmod 600 ~/.ssh/config
ssh-keyscan github.com >> ~/.ssh/known_hosts
ssh -T git@github.com        # expect: "Hi WillemKlopper87/ACS! You've successfully authenticated..."
git clone git@github.com:WillemKlopper87/ACS.git ~/ACS
git -C ~/ACS log -1 --oneline   # record this commit as the build under test
```

**3d. Install and start everything with one command:**

```bash
cd ~/ACS && GO_VERSION=1.26.10 ./scripts/quickstart.sh
```

`GO_VERSION=1.26.10` installs a newer Go than the repo's default 1.26.6. It closes 11 Go standard-library vulnerabilities that `govulncheck` reports as reachable in this code (net/http, crypto/tls, crypto/x509). That matters here because port 7547 faces the internet. The script finds the existing `~/ACS` clone and pulls through the deploy key.

It takes about 5–10 minutes on a t3.medium. It ends with **"Quick start complete — readiness gate passed."** and prints:

- `Console: http://<EC2_PUBLIC_IP>:5173`
- `CWMP URL: http://<EC2_PUBLIC_IP>:7547/cwmp`
- the console login username (`admin`)

If it stops early, rerun it. It's idempotent. Logs are in `~/acs-logs/`.

Then run the field preflight. It records the commit under test and blocks unsafe settings:

```bash
cd ~/ACS && ACS_FIELD_TEST_ACK=1 ./scripts/field-preflight.sh
```

Get the console admin password (privately):

```bash
grep ACS_BOOTSTRAP_ADMIN_PASSWORD ~/.acs-secrets.env
```

---

## 4. Get the exact values to type into the Nokia

**This is what keeps server and CPE in sync.** All values come from one file on the server, `~/.acs-secrets.env`, and this helper prints them in Nokia field order:

```bash
~/ACS/scripts/show-cpe-setup.sh --show-secrets
```

Map the output to the Nokia TR-069 page exactly like this:

| Nokia field | Value from `show-cpe-setup.sh` | Server variable it must equal |
|---|---|---|
| **ACS URL** | `http://<EC2_PUBLIC_IP>:7547/cwmp` | listener `ACS_ADDR=:7547` (the path after the port doesn't matter; the ACS accepts any path) |
| **ACS Username** | `acs-bootstrap` | `ACS_CWMP_BOOTSTRAP_USERNAME` |
| **ACS Password** | (random 32-char string) | `ACS_CWMP_BOOTSTRAP_PASSWORD` |
| **Periodic Inform** | Enabled | — |
| **Periodic Inform Interval** | `60` | — |
| **Connection Request Username** | `acs-connreq` | `ACS_CONNECTION_REQUEST_USERNAME` |
| **Connection Request Password** | (random) | `ACS_CONNECTION_REQUEST_PASSWORD` |
| Connection Request URL / port | **leave as the device sets it** | — |

Rules that avoid the classic mismatches:

- **Use `http://`, not `https://`.** The lab profile has no TLS certificate on 7547. An `https://` URL fails the TLS handshake and nothing shows in the ACS logs.
- **Use port 7547, not 5173 or 8080.** Those are the browser console and API, not CWMP.
- Copy the passwords exactly: no trailing spaces, and `+ / =` characters included (they're base64).
- Never put the ACS URL into the Connection Request URL field.
- Don't delete or regenerate `~/.acs-secrets.env` once the Nokia has connected. That strands its credentials.
- If you **stop/start** the instance, the public IP changes. Rerun `show-cpe-setup.sh` and update the Nokia's ACS URL.

Save/apply on the Nokia. Some firmware needs a reboot before it sends the first Inform.

---

## 5. Watch the first contact

Start this before you click Save on the Nokia:

```bash
~/ACS/scripts/logs.sh
```

This is what the logs should show, in order:

1. **POST → 401**: the Nokia connects and gets a Digest challenge (normal).
2. **Bootstrap Inform accepted**: the identity (OUI, ProductClass, serial) is recorded. **Note the remote IP here** and tighten SG port 7547 to it.
3. **The ACS sends SetParameterValues** on `Device.ManagementServer.Username/Password`. The Nokia gets a **new unique ACS password** that the ACS generated.
4. **The Nokia reconnects with the new credential**, its identity matches, and the credential goes **PENDING → ACTIVE**.
5. The device appears in the console (`http://<IP>:5173`) and normal discovery starts.

> ⚠️ **Step 3 is one-way, by design.** After graduation the ACS password stored on the Nokia is no longer `acs-bootstrap`/your bootstrap password, and the ACS **refuses the bootstrap login for that device from then on**. Don't re-enter the bootstrap password on the Nokia expecting it to reconnect. If graduation gets stuck (the Nokia rejects the SPV, or never reconnects), the device stays PENDING. Recover it through the operator credential workflow in the console; don't delete the device to "reset" it.

### What failure looks like

| Symptom in `logs.sh` | Cause | Fix |
|---|---|---|
| Nothing at all | SG 7547 closed, wrong IP/port, `https://` in URL, or Nokia not applied | Recheck the URL from step 4. From your PC, run `curl -v http://<IP>:7547/` and expect any HTTP response. |
| Repeated 401s, never accepted | Username/password mismatch, or the Nokia only does Basic auth | Re-copy the password. If it's still failing, check whether the Nokia sends `Authorization: Basic`. |
| `device data-model root is unknown` (409) | The Inform didn't reveal TR-181 vs TR-098 | Capture the Inform and stop there; don't hand-edit. This is qualification evidence. |
| SPV fault 9001/9008 on ManagementServer | This firmware won't let the ACS rewrite its ACS credentials | Record the fault. Graduation is BLOCKED for this firmware (expected possibility, see runbook §9). |
| Device online but "connection request failed" | Normal behind 5G CGNAT; the ACS can't reach the Nokia | Jobs run on the next periodic Inform (60 s). Connection Request testing is a later stage. |

---

## 6. Read-only qualification (after the device is ACTIVE)

On the EC2 box, using an operator API token from the console:

```bash
cd ~/ACS
export ACS_API_TOKEN="<operator token>"
python3 scripts/qualify-device.py \
  --base-url http://127.0.0.1:8080 \
  --device <DEVICE_UUID> \
  --scenario ci/compatibility/nokia-read-only.json \
  --output .local/nokia-onboarding/qualification.json
```

It is read-only: no writes, no reboot, and it skips IMSI/IMEI/ICCID-type paths.

**Don't** try APN, band lock, WAN auth, factory reset or firmware jobs on this CPE until the read-only stage and a reversible test write have passed. Losing the cellular link means a trip to the device.

---

## 7. Afterwards

- **Restore the Nokia** to the values you wrote down in step 0, if it belongs on a carrier ACS.
- Remove `0.0.0.0/0` from SG port 7547.
- **Stop** the instance when you're not testing. A t3.medium in af-south-1 costs roughly US$1.30/day running, and the IP changes on restart. **Terminate** it when you're done for good.
- Back up `~/.acs-secrets.env` before terminating if you want the same credentials on a rebuild.
