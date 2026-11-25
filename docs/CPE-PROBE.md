# CPE probe: what the FastMile returns

## Purpose

Phase 2 of the CPE telemetry design samples signal, link-change and location
data from the gateway. Before building it, we need to know what the lab Nokia
FastMile actually returns for those parameters: the value forms, the units,
whether the `LastChange` timers reset when the link drops, and what the
location leaves look like. The console's "Read parameters now" panel reads a
list of parameters on demand so this can be found out on the lab CPE. The
design is in `docs/superpowers/specs/2026-11-25-cpe-telemetry-design.md`
("Phase 1: probe"); this page is the procedure and the place the answers go.

## Running the probe

1. Open the lab CPE's device page and note its check-in interval: on the
   Connectivity page, in the Availability panel, the value shown as "Reports
   every". Write it in the Findings section below. A read runs at the CPE's
   next check-in, which is up to that long from now and may be later if a
   check-in is missed or the session is busy.
2. Open the device's Parameters page (left menu, under ADVANCED). Above the
   "Parameter cache" panel is the "Read parameters now" panel.
3. Tick "Include the location coordinates (read once, to learn their format)"
   first. The checkbox sits below the "Use FastMile telemetry preset" button.
   The preset only adds the coordinate paths when the box is already ticked,
   and "Read now" stays disabled while a coordinate path is in the text box
   and the box is unticked.
4. Click "Use FastMile telemetry preset". The "Paths to read, one per line"
   box fills with the signal leaves (RSRP, RSRQ, SNR, RSSI, band, cell ID and
   ARFCN under `X_ALU-COM_Attached5GCellStat` and
   `X_ALU-COM_AttachedLteCellStat`), the `LastChange` timers (Cellular,
   IP, Ethernet interface and Ethernet link), `Device.DeviceInfo.UpTime`,
   Cellular `NetworkInUse` and `RoamingStatus`, and `Device.DeviceInfo.Location.1`
   `Source`, `AcquiredTime` and `X_ALU-COM_TimeStamp`, plus the
   `X_ALU-COM_Latitude`, `Longitude` and `Altitude` leaves because the box is
   ticked. The preset lists only paths the device has already discovered.
5. Click "Read now". The read runs when the CPE next checks in: the CPE is
   behind carrier NAT (CGNAT), so the ACS cannot reach it on demand. After a
   successful send "Read now" unticks the coordinates box and removes the
   coordinate paths from the text box; the other paths stay.
6. After the check-in, find each path in the "Parameter cache" panel on the
   same page, using its search box.
7. The coordinates are read once. To read them again, tick the coordinates box
   again and click "Read now"; the paths are added when the preset is used or
   typed with the box ticked.

## The link-drop test

Run this on the lab CPE only. It cuts the cellular link and interrupts service
for several minutes. Never run it on a customer's device.

1. Note the time.
2. Read `Device.Cellular.Interface.1.LastChange` and `Device.DeviceInfo.UpTime`
   (type just these two paths, or use the preset with the coordinates box
   unticked), then click "Read now".
3. Wait for the check-in and confirm both values appear in the "Parameter
   cache" panel. Record them. Without this baseline the test says nothing.
4. Pull the SIM (or the antenna) for about five minutes, then reconnect it.
5. Wait for the CPE to come back and check in twice. Click "Read now" again
   (the two paths are still in the text box), then wait for that check-in and
   confirm the cache shows fresh values.
6. Record whether `LastChange` reset and by how much, and in what unit.
   `UpTime` should not have reset; if it did, the device rebooted, and the
   result says nothing about the link.

## What to record

One row per path read. Record how the value looks, not only what it is.

| Path | Value form | Unit | Notes |
|---|---|---|---|
| | | | |

Coordinates are recorded as a format only: the units, the precision (number of
decimal places), and what an empty or no-fix value looks like. Never write down
a position, not even approximate or rounded. No subscriber identifier (IMSI,
ICCID, MSISDN or IMEI) is written anywhere in this document either.

## Findings

Each answer is filled in after the lab run.

- CPE check-in interval ("Reports every"): to be filled in after the lab run.
- Does `LastChange` reset on a link drop, and in what unit: to be filled in
  after the lab run.
- Which of `IP.Interface.1` and `IP.Interface.2` carries the WAN address: to be
  filled in after the lab run.
- The `Location.1.Source` values, and whether a fix is present: to be filled in
  after the lab run.
- The coordinate format (units, precision, no-fix value): to be filled in
  after the lab run.
- Whether both the 5G and LTE signal leaves return values: to be filled in
  after the lab run.
- Any path that faulted: to be filled in after the lab run.
