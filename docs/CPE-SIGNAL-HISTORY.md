# CPE signal history

## What is sampled

For a cellular CPE the ACS records the signal readings it reports: RSRP,
RSRQ, SNR and RSSI, plus the band and the access technology. The values are
read from the device's own cellular parameters, the ones it has already
discovered (the same leaves the Parameters page lists).

A reading is taken at the device's check-ins. When a check-in session opens
and the device's last sample is older than the sample interval, the ACS queues
an ordinary parameter read in that session; the device answers it as part of
the same session, and the reply is stored as one sample. Nothing is written to
the CPE. The ACS cannot reach the CPE between check-ins (it sits behind
carrier NAT), so there is no reading between them.

Which metrics a sample holds depends on the CPE. A model that does not report
SNR, for example, has no SNR values, and the console shows that metric as
having no readings. A value the CPE reports as "no value" (such as -32768) or
that is not a finite number is stored as empty, not as a reading. A reply with
nothing usable is not stored.

The console shows the samples in the "Signal" panel on the device's
**Cellular & SIM** page: a chart per metric (RSRP, RSRQ, SNR, RSSI) over 24
hours, 7 days or 30 days, and a "Show as table" toggle that lists the same
rows, with the band and technology. The data is also available as
`GET /api/v1/devices/{id}/signal-history?hours=` (1 to 720 hours, 168 by
default).

## Resolution

The chart's resolution is the larger of the sample interval and the CPE's
check-in interval, because a sample can only be taken when the device checks
in. The check-in interval is shown on the device's Connectivity page, in the
Availability panel, as "Reports every".

When a range holds more than about 500 samples, they are averaged into at most
about 500 points; a point's time is the start of its bucket, and the panel
says how many minutes each step covers. Short ranges are shown as recorded.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `ACS_CPE_SAMPLE_INTERVAL` | 15 minutes | The least time between samples of one device. `0` turns sampling off. |
| `ACS_RETENTION_CPE_SAMPLES_DAYS` | 30 | How long samples are kept before they are pruned. `0` prunes nothing. |

The interval is a Go duration such as `15m` or `1h`. Raising the interval
lowers the resolution; it cannot make a device check in more often.

## What it does not store

- No subscriber identifiers (IMSI, ICCID, phone number) and no cell identity.
- No location.
- Nothing in the parameter cache or its history: the reply is recorded as a
  sample only, so the readings do not fill the parameter history.

Only devices that open check-in sessions (TR-069) are sampled. USP devices
open none, so they have no samples; their Signal panel stays empty.

## Checking it on the lab CPE

1. Update the ACS. After the first check-in that follows, a first point
   appears in the Signal panel.
2. A second point appears at the next check-in once the sample interval has
   passed. With the defaults and a check-in interval shorter than 15 minutes,
   that is the first check-in after 15 minutes; with a longer check-in
   interval, the next check-in.
3. If the panel stays empty:
   - On the device's Parameters page, check that the device has discovered its
     cellular parameters. Without them nothing is read.
   - Check that `ACS_CPE_SAMPLE_INTERVAL` is not `0`.
   - Look at the metric `acs_cpe_samples_total`, labelled by result:
     `recorded` is a stored sample, `empty` means the reply held no usable
     reading, and `failed` means storing it failed (see the ACS log).
