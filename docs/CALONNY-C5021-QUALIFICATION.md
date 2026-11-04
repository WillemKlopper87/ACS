# Calonny AX3000 5G router (C5021): qualification findings

Tested 2026-11-02 on one retail unit, firmware `C5021_V1.0`, from its web page
and a root shell. **Verdict: not qualified. The firmware has no TR-069 client, so
this ACS cannot manage it.** The unit also has security findings an ISP would
have to resolve before deploying it (below).

This page holds no passwords, serial numbers, SIM identifiers, APN, WAN
address or ACS address. Keep it that way when updating it.

## Who makes it

| | |
|---|---|
| Brand on the box | Calonny (a retail brand sold on Amazon and eBay) |
| Maker | **Stoneoim** (`DEVICE_MANUFACTURER` in `/etc/device_info`; "Shanghai Stoneoim Intelligent Technology Co." on stoneoim.com) |
| Model / revision | `C5021`, hardware revision `A1` |
| Listings | Disagree on ports (2 or 3, with or without an RJ11 phone port), speeds (2.6 or 4.61 Gbps) and even the model (one lists C503). The Amazon.de title names a Unisoc UDX710; **the unit does not use one** (see below), so the UDX710 vulnerabilities do not apply. |
| Manual | A third-party copy (manuals.plus). It is generic: it claims Wi-Fi 7 features an AX3000 router cannot have, never mentions TR-069 or remote management, and gives no default login. No official Calonny support or download site was found. |

## What is inside

| | |
|---|---|
| Software | Vendor build of **OpenWrt 21.02.7** ("V1.0"), `aarch64_cortex-a55`. OpenWrt 21.02 stopped getting security fixes in 2023. |
| 5G module | **Quectel RG600L-EU**, firmware `RG600LEUABR06A07G4G_OCPU_01.001.01.001`, built 2024-07-09 (`/etc/quectel-project-version`). |
| Platform | MediaTek (`gem6xxx/evb6890`): MediaTek 5G modem drivers and an MT7916 Wi-Fi chip. |
| Vendor layer | Stoneoim's own web page (LuCI plus `stoneoim` scripts), settings in `/etc/config/stoneoim`, and an update service `stoneoim_fota`. |

Quectel does not publish this firmware. Its RG600L/RG500L support threads say
the OpenCPU firmware goes only to the company that builds the router, so
updates can come only from Stoneoim.

Quectel's product change notice (PCN202502280001) says that from 1 August 2025
it ships RG600L-EU modules only with firmware upgraded for the EU radio
cybersecurity rules (RED DA). This unit's module firmware is older. Whether
the notice applies to a South African deployment is not established.

## TR-069: the page exists, the client does not

The router's web page has a **TR069 / ACS / Account / Password / Periodic notice
/ Notice interval** form. Saving it answered `status: 0` (success), but:

- `tr069_get` afterwards returned an empty body (before saving it returned a
  106-byte body).
- No TR-069 process runs (`ps | grep -iE "cwmp|tr06|tr69"` matches only itself).
- `/etc/config` has no TR-069 file (`cwmp`, `easycwmp`, `icwmp`, `tr069`), and
  `/etc/init.d` has no TR-069 start-up script.
- The system log (16 minutes, including after the save) holds **no TR-069
  line**. A working client logs every attempt to reach its ACS.
- No attempt to reach the ACS appears in the router's own log. (The ACS-side
  log was not captured, so "nothing arrived" is not verified.)

So the form saves into nothing. There is also no data model: no Inform, no
`InternetGatewayDevice` or `Device` tree to discover.

The form also has no Connection Request username or password fields. Even with
a client, the ACS could not knock on this router; it would have to wait for the
periodic Inform (the router sits behind carrier NAT).

## Security findings

| # | Finding | Severity | Evidence |
|---|---|---|---|
| 1 | **No TR-069 client**, despite a settings page for it | Blocks use | Above |
| 2 | **End-of-life base system**: OpenWrt 21.02.7, vendor V1.0, no public firmware, no stated support period (a sister Calonny manual says "Until Unknown") | High for a fleet | `/etc/openwrt_release` |
| 3 | **Firmware updates checked only by MD5.** The update tool downloads to `/tmp/firmware.bin`; the binary shows MD5 and no signature routine. A tampered image could be installed. | High | `strings`-style grep of `stoneoim_fota` |
| 4 | **Vendor management server.** The update tool is hard-wired to `dm.yunqitec.com` (a cloud-hosted address). The relationship between that name and Stoneoim is **not confirmed**. Whether the router contacts it, and what it sends, was not observed (no connection at test time; `fota_status.txt` empty). `/etc/device_info` holds only maker, model and revision. | To confirm | Binary strings, DNS, `netstat` |
| 5 | **Telnet on by default** with administrator access (`telnet_switch '1'`), on every interface; the vendor firewall rules block FTP, TFTP and ADB but not Telnet. | Medium | `netstat`, `iptables` |
| 6 | **TR-069 settings are sent in the URL.** The page saves with a `GET` whose query string carries the ACS URL, account and password, so they land in browser history and any proxy or server log. | Medium | Network capture of the web page |
| 7 | Management ports from the 5G side: **not reachable.** The 5G interface is in the `wan` zone with `input REJECT`; no public IPv6 address was assigned; IPv4 is behind carrier NAT. | OK | `uci show firewall`, `ip6tables`, `ip -6 addr` |
| 8 | The LAN zone accepts all input, so anyone on the router's Wi-Fi or Ethernet reaches the Telnet and SSH logins. With the default label password this is full control. | Medium | `uci show firewall` |

Treat this like the FastMile on the lab: no Wi-Fi passphrases or subscriber
data until the lab has TLS, and turn Telnet off and change the admin password
first.

## What was not tested

- Any CWMP or USP behaviour (there is none to test).
- The firmware update path end to end (nothing was installed; the update tool
  was only read).
- Whether `dm.yunqitec.com` is contacted over time, or with which protocol.
- Behaviour on a newer firmware.

## Questions for Stoneoim (send through Calonny or Amazon with the model, `C5021_V1.0` and ASIN B0DG1377YZ)

1. Is there a firmware for the C5021 with **TR-069 working**? Is **USP
   (TR-369)** available? Which client does it use?
2. What is the **latest firmware** for hardware revision A1, and is it built on
   a supported OpenWrt release and a current Quectel RG600L package (RED DA)?
3. How are firmware images **signed and verified** on the router? Do updates use
   HTTPS?
4. What is **`dm.yunqitec.com`**, who runs it and where? What does the router
   send to it? Can an ISP **turn it off** or point it at the ISP's own server?
5. Can **Telnet be off by default**, and the LAN-side firewall tightened?
6. How long will the C5021 receive security updates?
7. Do the TR-069 settings get saved over `POST` rather than a URL, with a
   Connection Request username and password?

## What would qualify it

A firmware that passes, in this order (the shared checklist is
`HUAWEI-CPE-QUALIFICATION-TEMPLATE.md`):

1. Saves the ACS settings and shows them back after a reboot.
2. Informs the ACS with the bootstrap login, receives the credential
   write, and reconnects with its own unique credential (bootstrap graduation).
3. Reports a data model the ACS can discover, with the Wi-Fi and cellular
   parameters writable.
4. Accepts a Connection Request, or sends reliable periodic Informs.
5. Telnet off by default, a verified update path, and a stated support period.

## Re-checking a new firmware (safe, read-only)

From a shell on the router:

```sh
cat /etc/openwrt_release /etc/quectel-project-version
ps | grep -iE "cwmp|tr06|tr69"
ls /etc/config /etc/init.d | grep -iE "cwmp|tr06|tr69|easycwmp|icwmp"
netstat -tln | grep -E ":23 |:22 "
```

Then save the TR-069 form and run `logread | grep -iE "cwmp|tr069"` to see
whether a client logs anything.
