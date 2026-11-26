# CPE signal history

## What is sampled

For a cellular CPE the ACS records the signal readings it reports: RSRP,
RSRQ, SNR and RSSI, plus the band and the access technology. The values are
read from the device's own cellular parameters, the ones it has already
discovered (the same leaves the Parameters page lists).

A reading is taken at the device's check-ins. When a check-in session opens
and a sample is due, the ACS queues an ordinary parameter read in that
session; the device answers it as part of the same session, and the reply is
stored as one sample. A sample is due once the sample interval, less a small
slack of one tenth of it (at most a minute), has passed since the last sample,
so a device that checks in every interval or faster is sampled at every
interval. Nothing is written to the CPE. The ACS cannot reach the CPE between
check-ins, so there is no reading between them.

When the CPE is attached only to LTE (or only to 5G), the readings come from
the object that reports them: the ACS reads both the 5G and the LTE leaves and
takes, per metric, the first real value, with the 5G object preferred when both
report. The band is taken from the object that supplied the RSRP. Band and
technology text from the CPE is stripped of control characters and cut to 32
characters.

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
in: a sample is due once the interval (less the slack above) has passed since
the last one, and the next check-in after that takes it. The check-in interval is shown on the device's Connectivity page, in the
Availability panel, as "Reports every".

When a range holds more than about 500 samples, they are averaged into at most
about 500 points; a point's time is the start of its bucket, and the panel
says how many minutes each step covers. Short ranges are shown as recorded.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `ACS_CPE_SAMPLE_INTERVAL` | 15 minutes | The least time between samples of one device. `0` turns sampling off. |
| `ACS_RETENTION_CPE_SAMPLES_DAYS` | 30 | How long samples are kept before they are pruned. `0` prunes nothing. |

The interval is a Go duration such as `15m` or `1h`. A value that does not
parse (such as `15`, with no unit) or is negative logs a warning naming the
variable and the ACS uses 15 minutes. Raising the interval
lowers the resolution; it cannot make a device check in more often.

## What it does not store

- No subscriber identifiers (IMSI, ICCID, phone number) and no cell identity.
- No location.
- Nothing in the parameter cache or its history: the reply is recorded as a
  sample only, so the readings do not fill the parameter history.

Only devices that open check-in sessions (TR-069) are sampled. USP devices
open none, so they have no samples; their Signal panel stays empty.
Devices on the TR-098 data model (an InternetGatewayDevice root) are not
sampled either, because there is no portable cellular object to read there.

## Checking it on the lab CPE

1. Deploy the new build (a restart also resets the per-process throttle).
   After the first check-in that follows, a first point appears in the Signal
   panel.
2. A second point appears at the first check-in once the sample interval, less
   the slack (one tenth, at most a minute), has passed since the first. With
   the defaults and a check-in interval of 15 minutes or less, that is the
   first check-in after about 14 to 15 minutes; with a longer check-in
   interval, the next check-in. This timing follows from how the sampler
   works; it has not been observed on a device. A point can be delayed while
   parameter discovery is running for the device, or if its parameter names
   have not been discovered yet, and a sample read that is already open holds
   the next one back until it is answered.
3. If the panel stays empty:
   - In the "Identity" panel on the device page, check "Data model root".
     `IGD1` is the TR-098 data model (`InternetGatewayDevice`), which is not
     sampled; `DEVICE2` is TR-181 (`Device`), which is.
   - On the device's Parameters page, check that the device has discovered its
     cellular parameters. Without them nothing is read.
   - Check that `ACS_CPE_SAMPLE_INTERVAL` is not `0`.
   - Look at the metric `acs_cpe_samples_total`, labelled by result, on the
     ACS gateway's `/metrics` endpoint (the device-facing listener; the
     production profile requires the bearer token in `ACS_METRICS_TOKEN`). `recorded` rising is the
     success signal. `empty` means the reply held no usable reading, and
     `failed` means storing it failed (see the ACS log).
   - Look for failed jobs created by `system:cpe-metrics` among the device's jobs. A stale or faulting parameter path makes the read fail at every
     interval, and a failed read does not move `acs_cpe_samples_total`.
