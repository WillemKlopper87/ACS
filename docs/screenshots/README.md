# Console screenshots

Current screens of the operator console, captured from a running stack.

| Folder | Captured | From | Notes |
|---|---|---|---|
| [`2026-11-15/`](2026-11-15/) | 15 November 2026 | `main` as of pull request 104 (`a36a4ba`), lab profile, NaaS integration on | Console after the redesign, two-step sign-in and the cookie session |
| [`../uat-screenshots/`](../uat-screenshots/) | 10 September 2026 | revision `d2ea8d1` | The figures of `docs/ACS_UAT_Readiness_Report.pdf`. Kept as they were: that report is a dated record and `scripts/build_uat_report.py` reads them by name. They show the console before the redesign. |

The data is lab data: 15 mock CPEs (Zyxel, Nokia, Huawei, Teltonika) and
test customers with generated names. 1500 px wide at 2x, full page, dark
theme.

## 2026-11-15

| File | Screen | Where in the console |
|---|---|---|
| `01-login` | Sign in | — |
| `02-dashboard` | Dashboard | Overview |
| `03-fleet-health` | Fleet health | Overview |
| `04-jobs` | Jobs | Activity |
| `05-alerting` | Alerting | Overview |
| `06-inventory` | Device inventory | Devices |
| `07-onboarding` | Onboarding (planned sales) | Devices |
| `08-fleet-control` | Bulk actions | Devices |
| `09-bulk-operations` | Bulk operations | Activity |
| `10-device-groups` | Groups | Devices |
| `11-config-templates` | Configuration templates | Configuration |
| `12-policies` | Policies | Configuration |
| `13-scheduled-jobs` | Scheduled jobs | Activity |
| `14-firmware-rollouts` | Firmware rollouts | Configuration |
| `15-session-captures` | Session captures | Records & tools |
| `16-bss-integration` | BSS / NaaS: mappings, webhooks, OAuth2 clients | Administration |
| `17-operators` | Operators | Administration |
| `18-tenancy` | Tenancy | Administration |
| `19-audit-log` | Audit log | Records & tools |
| `20-reports` | Reports | Records & tools |
| `21-help` | Help | Help |
| `22-device-overview` … `30-device-remote` | Device workspace: Overview, Wi-Fi, Cellular & SIM, Connectivity, Diagnostics, Firmware & files, Ownership, Parameters, Remote access | Devices → a device |
| `31-bulk-operation-detail` | One bulk operation with its per-device results | Activity |
| `32-account-menu` | Account, with two-step sign-in | the operator name, bottom left |

In `16-bss-integration` the account `SHARED-d0d75` appears under two
customers. That is the per-customer account registry at work
(`docs/TENANT-SCOPED-IDS-DESIGN.md`): the two rows are different accounts.

Not re-captured: the three Grafana dashboards (`19`–`21` in the September
set). They need the Compose monitoring stack, and the dashboards have not
changed.

## Capturing again

Start the stack (`scripts/start.sh`), sign in with Playwright at a
1500 × 900 viewport and `deviceScaleFactor: 2`, visit each route and take a
full-page screenshot. Put a new set in a new dated folder rather than
overwriting one.
