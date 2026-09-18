# Zyxel 5G CPE profile policy

Zyxel 5G devices must not be managed as one interchangeable profile. The
same product family can vary by region and firmware, particularly in its
TR-369 and diagnostic support. The ACS therefore uses a conservative
TR-181 Device:2 baseline before discovery, then persists the live CWMP
`GetParameterNames` or USP `GetSupportedDM` tree (including writeability).
The discovered tree is authoritative for all configuration and telemetry.

## Published family evidence

| Device / family | Published management capability | ACS posture |
| --- | --- | --- |
| NR7303-EU01V1F (NR7301/2/3 series) | TR-069, TR-369, TR-181, TR-143; its user-guide matrix marks TR-471 unavailable | Baseline catalog included. Use either CWMP or USP after transport discovery; do not schedule TR-471 by default. |
| NR7101/NR7102/NR7103 | TR-069 across the family; published TR-369 capability differs by product documentation and release | CWMP baseline; enable USP only after a successful USP session and `GetSupportedDM`. |
| NR5103 / NR5103E / NR5103EV3 | Legacy variants document TR-069/TR-181; EV3 documents TR-069 and TR-369 | Treat product class and software version as part of the profile key; enable USP only after live confirmation. |
| NR5307 / NR5309 | Current listings document TR-369; NR5309 also lists TR-069/TR-181 | Discovery-first; no assumptions about a shared diagnostic profile. |
| NR5111, NR5331, NR5333 | Current listings document TR-069/TR-369 and TR-143/TR-471 variants | Discovery-first; schedule performance tests only when the matching operation is advertised. |
| NR5731, NR7305, NR7307, NR7501 | Current listings document TR-369/TR-471 variants | USP-first where connected; preserve CWMP support if the deployed firmware also reports it. |

Sources: [NR7303 user guide](https://spdl.zyxel.com/NR7303/user_guide/NR7303_Users_guide_V1.00_Ed6_2023-10-24.pdf), [NR7303 product series](https://www.zyxel.com/service-provider/global/en/products/5g-nr4g-lte-cpe/5g-nr-cpe/odus/nr7301nr7302nr7303-series), and [Zyxel 5G portfolio](https://www.zyxel.com/service-provider/emea/en/products/5g-nr4g-lte-cpe?page=1). Product marketing and guides can differ by release, so these are onboarding hints rather than permission to send a command.

## Safe onboarding sequence

1. Capture manufacturer, OUI, product class, serial number, software version, and management transport from the initial Inform or USP OnBoard.
2. Run only one protocol-specific discovery: CWMP `GetParameterNames` under the reported root, or USP `GetSupportedDM` under `Device.`.
3. Persist full paths and USP access rights. Select `X_ZYXEL_*` extensions only from this evidence.
4. Allow a write only where the device reports the parameter as writable; otherwise reject it before a job is created.
5. Record the exact profile evidence against the firmware version before promoting it to a fleet default.

This policy lets newly released Zyxel 5G CPEs work immediately in
read/discovery mode, without risking configuration against guessed paths.
