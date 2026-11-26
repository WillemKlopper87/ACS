# SIM change detection

The ACS notices when the SIM in a cellular CPE is replaced and tells the
operator and the NaaS/BSS side. It only tells: nothing is changed on the CPE.
The design is in `docs/superpowers/specs/2026-11-26-sim-change-detection-design.md`.

## What is detected

The ACS compares the SIM identity a CPE reports (ICCID and IMSI of cellular
interface 1) with the one it knew before. Every reply that carries the
identity is judged, whoever asked for it (the console's "Read parameters now",
onboarding's SIM read, or the watch's own read).

| Situation | Result |
|---|---|
| Same SIM as before | nothing |
| The first SIM the ACS has seen on this device | remembered as the known SIM. If its network is not allowed, an alert |
| A different SIM on a network the plan allows | a **notice**: lower-priority incident, banner, event. Stays open until someone approves it |
| A different SIM on a network the plan does not allow | an **alert**: higher-priority incident, banner, event |
| An IMSI learned for the first time (it was not readable before) | judged against the allowed networks like a first SIM |

"Allowed" is the same rule onboarding uses (`allowed_plmns` of the sale's
service plan). A device with no in-service sale, no plan, or a plan with no
allowed networks allows every network, so it can only get a notice.

An empty or placeholder reading (a pulled SIM, a value the CPE cannot give)
never replaces the SIM the ACS knows. Reading nothing is not the same as the
SIM being removed.

## What the operator sees

- **Banner** at the top of the SIM panel on the device's Cellular & SIM page,
  showing the network and the masked ICCID and IMSI now and before.
- **Approve this SIM** (anyone with the devices-write permission). It asks for
  a reason of at least 10 characters, records it in the audit log with the
  operator's name, and accepts the SIM now in the device. The approval applies
  to the change that was shown: if the SIM changed again meanwhile, it is
  refused and the new change must be reviewed. Approving never writes to the CPE.
- **Incident** in the alerting system, once the device belongs to a customer
  account and an alert policy applies. Default priorities: alert P2, notice P4
  (a policy's fault priorities `sim_change_alert` and `sim_change_notice`
  override them). The incident recovers when the SIM is approved. A later
  change opens it again. A device with no account mapping, or no matching
  policy, gets the banner and the event but no incident.
- **Event** `SimChanged` to NaaS/BSS subscribers (verdict, network, masked
  hints). It is a contract addition pending the next contract release.

## What is stored and sent

Only the network (the first five digits of the IMSI) and masked hints (the last
few digits) are stored in `device_sim_watch`, sent in events, shown in
incidents and written to the audit log. The full IMSI, ICCID and MSISDN stay in
the parameter cache and behind the audited superadmin reveal, as before.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `ACS_SIM_WATCH_INTERVAL` | 1 hour | How often the ACS reads the SIM identity of a device at a check-in. `0` turns the watch's own read off (a reply that carries the identity is still judged). An invalid value (for example `60` without a unit) logs a warning naming the variable and uses 1 hour. |

## Limits

- Alert only. No lock, APN change or restriction is applied.
- CWMP devices, cellular interface 1 only.
- A swap while the CPE is offline is noticed at its next check-in; the read
  follows within the interval.
- The device needs its parameter tree discovered; a device with no SIM
  identity paths is not read.

## Checking it on the lab CPE

1. Open the Parameters page, "Read parameters now", tick nothing else and read
   `Device.Cellular.Interface.1.USIM.ICCID`, `…USIM.IMSI` and
   `…X_ALU-COM_CurrentUsedIMSI`. In the cache panel, note **which of the three
   hold a value** (record only which, never the values here). The watch reads
   the ICCID and the standard IMSI, and uses the Nokia IMSI when the standard
   one is empty. If none holds a value, detection cannot work on that CPE.
2. After the next check-in (up to the CPE's reporting interval, then the watch
   interval) the SIM panel shows no banner: the SIM is the known one.
3. With the owner's agreement, swap the SIM and wait for a check-in after it
   re-registers. A banner appears; with a mapped customer and a policy an
   incident opens. Approve it with a reason; the banner goes and the incident
   recovers within a minute.
4. Troubleshooting: a read that keeps failing shows as failed jobs created by
   `system:sim-watch` on the device's Jobs page. `device_sim_watch.last_checked_at`
   (database) shows when a reply last carried the identity.
