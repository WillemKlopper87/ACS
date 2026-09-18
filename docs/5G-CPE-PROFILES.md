# Cross-vendor 5G CPE profile policy

The ACS supports a broad fleet by treating a vendor/model profile as an
onboarding hint, never proof of a command or parameter. Live CWMP
`GetParameterNames` and USP `GetSupportedDM` discovery are the capability
authority. The persisted result drives telemetry selection and writeability
checks for BSS/OSS operations.

| Vendor | Publicly documented 5G CPE examples | Initial transport posture | Important gate |
| --- | --- | --- | --- |
| Huawei | 5G CPE Pro H112-370/H112-372, 5G CPE Pro 2 H122-373/H122-573, 5G CPE Win H312-371, N5368X | CWMP/TR-069 candidate; TR-143 optional | Do not assume USP, a Device:2 root, `X_HUAWEI_*` paths, or firmware file semantics. |
| Nokia | FastMile Gateway 2, 3, 3.1, 3.2 | CWMP/TR-069 candidate | Public FastMile material does not establish USP support; require a working USP connection before enabling it. |
| ZTE | MC801, MC888, MC889, MU5120 | CWMP/TR-069 candidate | Consumer and operator firmware are different profiles; do not infer USP or a TR-181 revision from the model name. |
| TP-Link | HX716 Pro, HX710 Pro/HX710, HX510 variants, HX220, HX141, HC220-G5, NE210-Outdoor, NX810v | Prefer USP/TR-369 when connected; retain CWMP | Most ISP models list TR-181 and TR-069/TR-369, but feature firmware is required and TR-098/TR-111/TR-143 remain model-specific. |
| Zyxel | NR7303 and related NR/NR5xx families | Use confirmed CWMP or USP transport | See [Zyxel policy](ZYXEL-5G-PROFILES.md); test/diagnostic support varies by release. |

## What the ACS enforces

- Standard `Device.` TR-181 cellular diagnostics are the pre-discovery
  fallback for every listed vendor. Huawei additionally retains its legacy
  TR-098 baseline.
- ZTE and TP-Link now have explicit conservative catalog entries, and
  manufacturer matching normalizes strings such as `TP-Link`, `TP Link`,
  and legal-suffix variants.
- Once discovery completes, actual parameters -- including vendor extensions
  such as `X_HUAWEI_*`, `X_NOKIA_*`, `X_ZTE_*`, or `X_TP_LINK_*` -- replace
  static guesses for cellular telemetry.
- USP access rights are stored and a write is only selected when the agent
  advertises it as read/write or write-only. CWMP write paths are similarly
  checked against its discovered tree by BSS/OSS action translation.
- Diagnostics, firmware transfer, and optional protocols are feature-gated by
  the discovered model; a product name alone never enables them.

## Source material

- [Huawei 5G FWA / TR-069 and TR-143](https://carrier.huawei.com/en/success-stories/home-broadband/internet-for-all)
- [Nokia FastMile Gateway 3 guide](https://www.nokia.com/sites/default/files/2022-11/fastmile-5g-gateway-3-user-guide.pdf)
- [ZTE TR-069 CPE operations](https://www.zte.com.cn/global/about/magazine/zte-technologies/2021/4-en/special-topic/2.html)
- [TP-Link ISP Product Guide](https://static.tp-link.com/upload/smb/2024/202405/20240510/ISP%20Product%20Guide_English_2308.pdf)
- [TP-Link Aginet / TAUC](https://www.tp-link.com/nordic/landing/aginet-solution/)

Public specifications vary by carrier and firmware. Before promoting a
profile to a production default, capture an onboarding session, discover the
model, and validate one read, one permitted write, reboot, and firmware flow
against a non-production unit.
