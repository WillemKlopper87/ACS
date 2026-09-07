import { useMemo, useState, type ReactNode } from "react";
import { useAuth } from "../auth/useAuth";

// In-app operator handbook. It lives in the console rather than in a
// markdown file because the questions it answers ("why is my job still
// QUEUED", "what does UNREACHABLE actually mean") arrive while someone is
// looking at the screen that prompted them.
//
// Everything here is written against the behaviour the code actually has,
// not the design documents — where the two differ (TR-098 writes, XMPP,
// per-session SOAP transcripts) the limitation is stated plainly instead of
// being left for someone to discover during an incident.

type Audience = "everyone" | "admin";

type Section = {
  id: string;
  title: string;
  group: string;
  audience: Audience;
  keywords: string;
  body: ReactNode;
};

function Term({ children }: { children: ReactNode }) {
  return <code className="help-term">{children}</code>;
}

function Steps({ items }: { items: ReactNode[] }) {
  return (
    <ol className="help-steps">
      {items.map((item, i) => (
        <li key={i}>{item}</li>
      ))}
    </ol>
  );
}

const SECTIONS: Section[] = [
  // ---------------------------------------------------------------- start
  {
    id: "orientation",
    group: "Getting started",
    title: "What this console is",
    audience: "everyone",
    keywords: "overview acs tr-069 cwmp cpe introduction start here",
    body: (
      <>
        <p>
          This is the operator console for a TR-069 (CWMP) Auto Configuration Server. It manages
          <strong> CPE</strong> — the routers and ONTs in your subscribers' homes. The ACS does not
          reach into a device whenever it likes: TR-069 is a <em>device-initiated</em> protocol. The
          CPE calls the ACS, and the ACS answers with whatever work is queued.
        </p>
        <p>
          That single fact explains most of what feels surprising at first, and it is worth reading
          <a href="#latency"> Why is my job still QUEUED</a> before you use anything else here.
        </p>
        <p className="help-note">
          The sidebar is grouped by intent: <strong>Monitor</strong> to see what the fleet is doing,
          <strong> Devices</strong> to act on specific hardware, <strong>Provisioning</strong> to
          change how devices get configured, <strong>Administration</strong> to run the platform,
          and <strong>Records</strong> for history and exports.
        </p>
      </>
    ),
  },
  {
    id: "roles",
    group: "Getting started",
    title: "Roles and what each one can do",
    audience: "everyone",
    keywords: "rbac role permission readonly noc manager superadmin access denied 403",
    body: (
      <>
        <p>There are four roles, in increasing order of privilege:</p>
        <table className="help-table">
          <thead>
            <tr>
              <th>Role</th>
              <th>Can do</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td><Term>readonly</Term></td>
              <td>See everything in scope. No writes at all — buttons that change something are disabled.</td>
            </tr>
            <tr>
              <td><Term>noc</Term></td>
              <td>Day-to-day operations: run diagnostics, send connection requests, read parameters.</td>
            </tr>
            <tr>
              <td><Term>manager</Term></td>
              <td>The above plus configuration: templates, policies, schedules, firmware rollouts, groups.</td>
            </tr>
            <tr>
              <td><Term>superadmin</Term></td>
              <td>Everything, including operators, tenancy and the BSS integration. Always sees the whole estate.</td>
            </tr>
          </tbody>
        </table>
        <p>
          Permissions are enforced by the API, not just hidden in the UI. If a button is greyed out,
          your role does not carry that permission — ask a superadmin rather than looking for a way
          around it.
        </p>
      </>
    ),
  },
  {
    id: "first-login",
    group: "Getting started",
    title: "Your first login, and forgotten passwords",
    audience: "everyone",
    keywords: "login password reset forgot sign in account disabled bootstrap admin",
    body: (
      <>
        <p>
          The very first account is created from the server's environment when the database is empty
          (<Term>ACS_BOOTSTRAP_ADMIN_USERNAME</Term> / <Term>ACS_BOOTSTRAP_ADMIN_PASSWORD</Term>).
          After that, accounts are made in <strong>Administration → Operators</strong>.
        </p>
        <p>
          Forgotten password: use the link on the sign-in screen. If no SMTP server is configured the
          reset link is written to the API's log instead of being emailed, so in a lab you may need
          an administrator to read it out of the logs. A superadmin can also reset a password
          directly, or take an account out of service — the last active superadmin cannot be
          disabled, deliberately.
        </p>
        <p>
          Sessions are JWT-based and expire. Signing out revokes the token server-side, so it cannot
          be replayed.
        </p>
      </>
    ),
  },

  // ------------------------------------------------------------ onboarding
  {
    id: "onboarding",
    group: "Devices",
    title: "Onboarding a device",
    audience: "everyone",
    keywords: "onboard add device new cpe register inform bootstrap provision setup connect",
    body: (
      <>
        <p>
          <strong>You do not add devices in this console.</strong> A device appears by itself the
          first time it successfully calls in. Your job is to point it at the ACS and give it
          credentials.
        </p>
        <Steps
          items={[
            <>
              On the CPE's TR-069 / Remote Management page, set <Term>ManagementServer.URL</Term> to
              your ACS address on port <Term>7547</Term>. The path does not matter — the gateway
              accepts any path, so a device configured with <Term>/cwmp</Term>, <Term>/acs</Term> or
              just <Term>/</Term> all work.
            </>,
            <>
              Set <Term>ManagementServer.Username</Term> and <Term>Password</Term> to the ACS's CPE
              credentials (the <Term>ACS_DIGEST_USERNAME</Term> / <Term>ACS_DIGEST_PASSWORD</Term>
              pair, or a per-device credential if you have issued one).
            </>,
            <>
              Enable <Term>PeriodicInformEnable</Term> and set{" "}
              <Term>PeriodicInformInterval</Term> to <strong>60–300 seconds while testing</strong>.
              See <a href="#latency">why this matters</a>.
            </>,
            <>
              Set <Term>ConnectionRequestUsername</Term> / <Term>Password</Term> so the ACS can wake
              the device on demand instead of waiting for the next Inform.
            </>,
            <>
              Save. The CPE should send a <Term>0 BOOTSTRAP</Term> Inform within moments — that is
              the event that triggers auto-provisioning and parameter discovery.
            </>,
          ]}
        />
        <p>
          Then check <strong>Devices → Device Fleet</strong>. The device should appear with a recent
          Last Inform. If nothing arrives, work through{" "}
          <a href="#device-not-appearing">A device is not appearing</a>.
        </p>
        <p className="help-note">
          Pre-registering devices ahead of an install is optional and is done in bulk from{" "}
          <strong>Administration → Tenancy</strong> (JSON, CSV or XML import). That reserves the
          customer and tags; the device still fills in its own details on first Inform.
        </p>
      </>
    ),
  },
  {
    id: "latency",
    group: "Devices",
    title: "Why is my job still QUEUED?",
    audience: "everyone",
    keywords: "queued stuck pending job not running slow latency periodic inform waiting",
    body: (
      <>
        <p>
          Because the device has not called in yet. Every action you queue — set a parameter, reboot,
          run a diagnostic, push firmware — sits at <Term>QUEUED</Term> until the CPE opens its next
          session. The ACS then dispatches the work inside that session.
        </p>
        <p>
          <strong>Your periodic Inform interval is your action latency.</strong> If a device informs
          hourly, a queued reboot may sit for an hour. That is TR-069 working normally, not a fault.
        </p>
        <p>
          To act now, use <strong>Connection Request</strong> from the device's detail panel. That
          asks the CPE to open a session immediately. It needs the device to be reachable — see{" "}
          <a href="#reachability">Reachability</a>.
        </p>
      </>
    ),
  },
  {
    id: "statuses",
    group: "Devices",
    title: "ONLINE, OFFLINE and UNREACHABLE",
    audience: "everyone",
    keywords: "status online offline unreachable liveness stale last inform meaning",
    body: (
      <>
        <p>Status is derived from the last Inform time, not from a live connection:</p>
        <table className="help-table">
          <thead>
            <tr><th>Status</th><th>Meaning</th></tr>
          </thead>
          <tbody>
            <tr><td><Term>ONLINE</Term></td><td>Informed within the last 5 minutes.</td></tr>
            <tr><td><Term>OFFLINE</Term></td><td>Last Inform between 5 and 90 minutes ago.</td></tr>
            <tr><td><Term>UNREACHABLE</Term></td><td>No Inform for over 90 minutes.</td></tr>
          </tbody>
        </table>
        <p>
          A device with a 60-minute periodic interval will therefore sit at <Term>OFFLINE</Term> most
          of the time and that is perfectly healthy. Judge devices against their own interval —
          <Term>UNREACHABLE</Term> is the status worth chasing.
        </p>
      </>
    ),
  },
  {
    id: "reachability",
    group: "Devices",
    title: "Reachability and Connection Requests",
    audience: "everyone",
    keywords: "connection request nat cgnat stun reachability push instant wake udp annex g",
    body: (
      <>
        <p>
          A Connection Request is the ACS calling the CPE, which only works if the device is
          actually reachable from the ACS. The reachability column tells you which case you are in:
        </p>
        <ul className="help-list">
          <li><Term>DIRECT_IPV4</Term> / <Term>DIRECT_IPV6</Term> — the ACS can dial the device directly. Connection Requests work.</li>
          <li><Term>PERIODIC_FALLBACK_ONLY</Term> — the device is behind NAT/CGNAT with no usable path in. You wait for its periodic Inform.</li>
          <li><Term>UNKNOWN</Term> — not yet established.</li>
        </ul>
        <p>
          For NAT'd fleets the ACS also supports Annex G UDP connection requests using a STUN-learned
          address. This is implemented from the specification but{" "}
          <strong>has not yet been validated against real hardware</strong> — treat it as unproven
          until you have confirmed it on your own devices.
        </p>
      </>
    ),
  },
  {
    id: "device-detail",
    group: "Devices",
    title: "What you can do to a single device",
    audience: "everyone",
    keywords: "device detail panel parameters reboot factory reset ping traceroute ssh console webgui vpn location",
    body: (
      <>
        <p>
          Click any row in <strong>Device Fleet</strong> to open its detail panel. The link is
          shareable — the selected device is kept in the URL.
        </p>
        <ul className="help-list">
          <li><strong>Parameters</strong> — the cached data model, with change history. "Live read" queues a fresh read from the device.</li>
          <li><strong>Discovery</strong> — asks the CPE what parameters it supports. Runs automatically on first BOOTSTRAP.</li>
          <li><strong>Diagnostics</strong> — ping and traceroute run <em>on the device</em>, which is how you prove a subscriber's line rather than your own.</li>
          <li><strong>Reboot / Factory reset</strong> — factory reset is destructive and unrecoverable from here. Be sure.</li>
          <li><strong>Console / Web GUI / VPN</strong> — SSH or Telnet through the browser, a reverse proxy to the device's own web interface, and WireGuard enrolment.</li>
          <li><strong>Location</strong> — a free-text site label plus optional latitude/longitude. The coordinates are what place the device on the Grafana site map, so fill them in during installation.</li>
        </ul>
        <p className="help-note">
          SSH host keys are pinned on first connect. If a device's key legitimately changes (after a
          firmware flash, say) an administrator must clear the stored key before you can connect
          again — that refusal is the feature working.
        </p>
      </>
    ),
  },
  {
    id: "groups",
    group: "Devices",
    title: "Groups: acting on many devices at once",
    audience: "everyone",
    keywords: "group add devices membership bulk select target template policy schedule rollout",
    body: (
      <>
        <p>
          A group is a named set of devices that templates, schedules and rollouts can target. Create
          one in <strong>Devices → Groups</strong>, then click it to manage membership.
        </p>
        <p>Two ways to add devices:</p>
        <ul className="help-list">
          <li>
            <strong>Add devices…</strong> opens a picker. Search by serial, vendor, model or site,
            tick what you want, and "Select all N" applies to whatever your search currently matches
            — so search first, then select all.
          </li>
          <li>
            <strong>Paste serial numbers</strong> for spreadsheet or ticket workflows. Comma, space
            or newline separated. Anything that does not match a known device is reported back to
            you rather than silently skipped.
          </li>
        </ul>
        <p>
          Deleting a group does not delete devices, but any template, schedule or rollout pointing at
          it loses its target — which is why the console asks you to confirm.
        </p>
      </>
    ),
  },
  {
    id: "fleet-control",
    group: "Devices",
    title: "Fleet Control and bulk actions",
    audience: "everyone",
    keywords: "fleet control bulk action mass set parameter connection request select all matching",
    body: (
      <>
        <p>
          <strong>Fleet Control</strong> is for doing one thing to many devices. Filter the fleet,
          then either tick rows or use "select all N matching" — which acts on every device matching
          the filter, including ones not on the current page.
        </p>
        <p>
          Bulk actions are deliberately limited to <Term>SET_PARAMETER</Term>,{" "}
          <Term>CONNECTION_REQUEST</Term> and a cellular refresh. Reboots and factory resets are not
          available in bulk. Each selected device gets its own job, so results arrive independently —
          watch them in <strong>Monitor → Jobs</strong>.
        </p>
      </>
    ),
  },

  // ---------------------------------------------------------- provisioning
  {
    id: "templates",
    group: "Provisioning",
    title: "Config templates and zero-touch provisioning",
    audience: "everyone",
    keywords: "template config zero touch auto apply bootstrap provisioning new device defaults",
    body: (
      <>
        <p>
          A template is a set of parameter values applied together. Templates can be applied on
          demand to a group, or marked to auto-apply when a device sends its first{" "}
          <Term>0 BOOTSTRAP</Term> — which is how a device gets configured with no one touching it.
        </p>
        <p className="help-note">
          Writes target the TR-181 (<Term>Device.</Term>) data model. Devices that only speak TR-098
          (<Term>InternetGatewayDevice.</Term>) can be read but <strong>not written</strong>. Check
          the data model column before you build a template for a mixed fleet.
        </p>
      </>
    ),
  },
  {
    id: "policies",
    group: "Provisioning",
    title: "Policies: keeping settings from drifting",
    audience: "everyone",
    keywords: "policy compliance drift enforce every inform reapply unsolicited change",
    body: (
      <>
        <p>
          A template sets a value once. A <strong>policy</strong> keeps it that way: it is evaluated
          on every Inform, and if a device has drifted the ACS re-applies the intended value and
          records the change.
        </p>
        <p>
          Use policies for things that must never differ — management credentials, remote access
          settings — and templates for initial setup. A change made on the device by a subscriber or
          a field engineer will be reverted by a policy, which is usually the point, but do check
          before enabling one across a live fleet.
        </p>
      </>
    ),
  },
  {
    id: "schedules",
    group: "Provisioning",
    title: "Scheduled jobs",
    audience: "everyone",
    keywords: "schedule cron recurring maintenance window nightly job type",
    body: (
      <>
        <p>
          Schedules run a job against a group on a repeating basis — a nightly parameter read, a
          weekly diagnostic. Not every job type can be scheduled; the console rejects ones that a
          schedule cannot meaningfully run rather than accepting them and failing later.
        </p>
        <p>
          Scheduled work is still subject to <a href="#latency">session timing</a>: the job is queued
          on schedule, then dispatched at each device's next session.
        </p>
      </>
    ),
  },
  {
    id: "rollouts",
    group: "Provisioning",
    title: "Firmware rollouts",
    audience: "everyone",
    keywords: "firmware upgrade rollout canary batch rollback download transfer complete image",
    body: (
      <>
        <p>
          Upload an image, choose a target group, then advance the rollout in stages rather than all
          at once:
        </p>
        <ul className="help-list">
          <li><strong>Canary percentage</strong> — how much of the group gets each wave.</li>
          <li><strong>Maximum failure rate</strong> — the rollout halts itself if failures exceed this.</li>
          <li><strong>Rollback image</strong> — the version to return to if a wave goes badly.</li>
        </ul>
        <p>
          A firmware push is a <Term>Download</Term> RPC plus a signed, expiring URL the device
          fetches from. A device that accepts the RPC but never reports{" "}
          <Term>TransferComplete</Term> usually cannot reach that URL — check the device's route to
          the ACS before blaming the image. You can also push firmware to a single device from its
          detail panel when you are testing.
        </p>
      </>
    ),
  },

  // -------------------------------------------------------- administration
  {
    id: "tenancy",
    group: "Administration",
    title: "Tenancy: regions, customers and projects",
    audience: "admin",
    keywords: "tenancy region customer project scope multi tenant isolation assign operator",
    body: (
      <>
        <p>
          The ownership hierarchy is <strong>region → customer → device</strong>. Each device belongs
          to at most one customer. <strong>Projects</strong> are separate: cross-cutting tags a
          device can carry several of, for things like a rollout programme.
        </p>
        <p>
          Operators are restricted by <strong>scopes</strong>. A scope grants a region (covering
          every customer under it) or a single customer.
        </p>
        <p className="help-note">
          Important: an operator with <strong>no scopes has no access</strong>, not full access. When
          you create an account you must also give it scopes, or grant the explicit global-access
          entitlement. Superadmins are always global regardless of scopes.
        </p>
        <p>
          Devices that belong to no customer are invisible to every scoped operator. The Grafana
          locator dashboard has an "Unassigned devices" tile specifically so they do not get lost.
        </p>
      </>
    ),
  },
  {
    id: "operators",
    group: "Administration",
    title: "Managing operator accounts",
    audience: "admin",
    keywords: "operator user account create disable offboard role permission matrix",
    body: (
      <>
        <p>
          <strong>Administration → Operators</strong> creates accounts, sets roles, assigns tenancy
          scopes and shows the permission matrix for each role.
        </p>
        <p>
          To offboard someone, <strong>disable</strong> the account rather than deleting it — that
          preserves the audit trail and immediately revokes their live sessions. The system refuses
          to disable the last active superadmin so you cannot lock everyone out.
        </p>
      </>
    ),
  },
  {
    id: "bss",
    group: "Administration",
    title: "BSS / CRM integration",
    audience: "admin",
    keywords: "bss crm oauth client order webhook mapping account provisioning api integration",
    body: (
      <>
        <p>
          The BSS adapter is a separate service that lets your billing or CRM system drive the ACS.
          It authenticates callers with OAuth2 client credentials (issue clients here) or a shared
          token, and it maps a billing account to a device.
        </p>
        <ul className="help-list">
          <li><strong>Mappings</strong> — account ID to device, so the BSS never needs to know serial numbers.</li>
          <li><strong>Orders</strong> — an instruction such as a Wi-Fi change. Idempotent on the external order ID, so a retry from the BSS will not double-apply.</li>
          <li><strong>Webhooks</strong> — HMAC-signed callbacks telling the BSS when a job finished, with retry and backoff.</li>
        </ul>
        <p className="help-note">
          Suspend/activate orders are rejected until a walled-garden parameter is configured on the
          server. There is no parameter that means "suspend" across all vendors, so this is
          deliberately left for you to define rather than guessed at.
        </p>
      </>
    ),
  },

  // -------------------------------------------------------------- records
  {
    id: "audit-reports",
    group: "Records",
    title: "Audit log and reports",
    audience: "everyone",
    keywords: "audit log history who did what report export excel xlsx compliance evidence",
    body: (
      <>
        <p>
          The <strong>Audit Log</strong> records operator actions and significant system events —
          logins, queued jobs, policy enforcement, template application, device Informs. It is the
          answer to "who changed this and when".
        </p>
        <p>
          <strong>Reports</strong> exports the fleet to a real Excel workbook: status, firmware, SSID,
          identity, location, customer and region, filtered by tenancy and by your own scope.
        </p>
      </>
    ),
  },
  {
    id: "dashboards",
    group: "Records",
    title: "Grafana dashboards",
    audience: "everyone",
    keywords: "grafana dashboard metrics map locator bss fleet monitoring prometheus",
    body: (
      <>
        <p>Three dashboards ship with the platform, alongside this console:</p>
        <ul className="help-list">
          <li><strong>ACS CPE Fleet</strong> — liveness, Inform and session rates, job outcomes, vendor and data-model mix.</li>
          <li><strong>ACS BSS Integration</strong> — order outcomes, webhook delivery backlog, adapter traffic and latency.</li>
          <li><strong>ACS Tenancy &amp; Site Locator</strong> — a map of your devices coloured by status, filtered by region and customer, with a "devices to visit" table sorted by longest outage. Each row links back into this console.</li>
        </ul>
        <p className="help-note">
          A device only appears on the map if it has latitude and longitude set on its detail panel.
          Capturing coordinates at installation time is what makes the locator useful later.
        </p>
      </>
    ),
  },

  // ------------------------------------------------------ troubleshooting
  {
    id: "device-not-appearing",
    group: "Troubleshooting",
    title: "A device is not appearing",
    audience: "everyone",
    keywords: "not appearing missing device no inform 401 auth failed cannot connect troubleshoot fault find",
    body: (
      <>
        <p>
          A device that has never successfully informed <strong>does not exist in this console</strong>
          — there is no row to inspect, so the console cannot tell you why. Work outwards:
        </p>
        <Steps
          items={[
            <>
              <strong>Is the ACS reachable from the device's network?</strong> Test from outside your
              LAN — a phone on mobile data, not office Wi-Fi — to rule out a firewall or NAT problem
              independently of the device.
            </>,
            <>
              <strong>Is the URL exactly right?</strong> Host and port <Term>7547</Term>. The path is
              not the problem; the host, port or scheme usually is.
            </>,
            <>
              <strong>Are the credentials right?</strong> A wrong password produces a repeating 401.
              Devices often cache an old ACS password from a previous provider.
            </>,
            <>
              <strong>Ask an administrator to check the gateway log.</strong> Failed authentication is
              only visible there — it produces no audit entry and no device row. This is a known gap,
              not something you are missing in the UI.
            </>,
          ]}
        />
        <p className="help-note">
          Administrators: the CWMP gateway can be put into a verbose onboarding mode that logs every
          request reaching the endpoint, which separates "wrong credentials" from "never arrived".
          Sustained 4xx also now shows on the CPE Fleet dashboard and raises an alert.
        </p>
      </>
    ),
  },
  {
    id: "job-failed",
    group: "Troubleshooting",
    title: "A job failed — reading the fault",
    audience: "everyone",
    keywords: "job failed fault code error retry lease expired dead letter timeout",
    body: (
      <>
        <p>
          Open <strong>Monitor → Jobs</strong> and read the fault code, which comes from the CPE
          itself. A few worth knowing:
        </p>
        <ul className="help-list">
          <li>
            <Term>LEASE_EXPIRED_UNSAFE_RETRY</Term> — a destructive action (reboot, factory reset,
            firmware) lost its lease mid-flight and was <strong>deliberately not retried</strong>. The
            work did not run and will not. Re-queue it yourself once you know the device's state.
          </li>
          <li>
            Invalid parameter name — usually a TR-181 path sent to a TR-098 device. Check the data
            model column.
          </li>
          <li>
            A firmware job that never completes — the CPE could not fetch the image URL, or the
            signed URL expired before it tried.
          </li>
        </ul>
      </>
    ),
  },
  {
    id: "limits",
    group: "Troubleshooting",
    title: "Known limitations",
    audience: "everyone",
    keywords: "limitation unsupported not implemented xmpp tr-098 write transcript session soap gap",
    body: (
      <>
        <p>Worth knowing before you go looking for something that is not there:</p>
        <ul className="help-list">
          <li><strong>TR-098 writes are not supported.</strong> Legacy devices can be read but not configured.</li>
          <li><strong>XMPP connection requests are not implemented.</strong></li>
          <li><strong>There is no per-session SOAP transcript.</strong> When a device behaves oddly, the exact request and response are not retrievable from the console afterwards — only the parsed result. Vendor interoperability problems currently need server-side logs.</li>
          <li><strong>Failed CPE authentication leaves no trace in the console.</strong> See <a href="#device-not-appearing">A device is not appearing</a>.</li>
          <li><strong>Annex G UDP connection requests are unvalidated</strong> against real hardware.</li>
        </ul>
      </>
    ),
  },
  {
    id: "glossary",
    group: "Troubleshooting",
    title: "Glossary",
    audience: "everyone",
    keywords: "glossary terms cwmp inform rpc oui serial data model tr-181 tr-098 definitions",
    body: (
      <table className="help-table">
        <thead>
          <tr><th>Term</th><th>Meaning</th></tr>
        </thead>
        <tbody>
          <tr><td><Term>CPE</Term></td><td>Customer Premises Equipment — the subscriber's router or ONT.</td></tr>
          <tr><td><Term>CWMP</Term></td><td>The protocol TR-069 defines. Used interchangeably with "TR-069" here.</td></tr>
          <tr><td><Term>Inform</Term></td><td>The message a CPE sends to open a session. Everything starts with one.</td></tr>
          <tr><td><Term>0 BOOTSTRAP</Term></td><td>The event meaning "my ACS configuration changed" — first contact. Triggers auto-provisioning.</td></tr>
          <tr><td><Term>2 PERIODIC</Term></td><td>The routine scheduled check-in.</td></tr>
          <tr><td><Term>RPC</Term></td><td>A single instruction to a device inside a session, e.g. SetParameterValues.</td></tr>
          <tr><td><Term>TR-181 / Device.</Term></td><td>The modern data model. Readable and writable.</td></tr>
          <tr><td><Term>TR-098 / InternetGatewayDevice.</Term></td><td>The legacy data model. Read-only here.</td></tr>
          <tr><td><Term>OUI</Term></td><td>The manufacturer prefix; with product class and serial it forms a device's unique identity.</td></tr>
          <tr><td><Term>Connection Request</Term></td><td>The ACS asking a CPE to start a session now.</td></tr>
        </tbody>
      </table>
    ),
  },
];

export function Help() {
  const { role } = useAuth();
  const isAdmin = role === "superadmin";
  const [query, setQuery] = useState("");

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return SECTIONS.filter((s) => {
      // Admin-only sections stay listed for everyone else only if they are
      // actively searched for; hiding them entirely invites "the docs don't
      // mention tenancy at all" while showing them invites "why can't I".
      if (s.audience === "admin" && !isAdmin && !q) return false;
      if (!q) return true;
      return (
        s.title.toLowerCase().includes(q) ||
        s.keywords.includes(q) ||
        s.group.toLowerCase().includes(q)
      );
    });
  }, [query, isAdmin]);

  const groups = useMemo(() => {
    const out: { group: string; items: Section[] }[] = [];
    for (const s of visible) {
      const existing = out.find((g) => g.group === s.group);
      if (existing) existing.items.push(s);
      else out.push({ group: s.group, items: [s] });
    }
    return out;
  }, [visible]);

  return (
    <section className="help">
      <div className="panel">
        <h3>Help &amp; operator handbook</h3>
        <p className="dim" style={{ marginTop: 0 }}>
          How this ACS behaves, what each screen is for, and what to do when something looks wrong.
          New here? Start with <a href="#orientation">What this console is</a> and{" "}
          <a href="#onboarding">Onboarding a device</a>.
        </p>
        <div className="form-row">
          <input
            aria-label="Search help topics"
            placeholder="Search help — try &quot;queued&quot;, &quot;offline&quot;, &quot;group&quot;, &quot;firmware&quot;…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="btn sm ghost" onClick={() => setQuery("")}>
              Clear
            </button>
          )}
        </div>
      </div>

      {visible.length === 0 ? (
        <div className="panel">
          <p className="dim" style={{ margin: 0 }}>
            Nothing matches “{query}”. Try a symptom rather than a feature — “queued”, “offline”,
            “401”, “not appearing”.
          </p>
        </div>
      ) : (
        <div className="help-layout">
          <nav className="help-toc panel" aria-label="Help contents">
            <h3>Contents</h3>
            {groups.map((g) => (
              <div key={g.group} className="help-toc-group">
                <div className="help-toc-label">{g.group}</div>
                {g.items.map((s) => (
                  <a key={s.id} href={`#${s.id}`}>
                    {s.title}
                  </a>
                ))}
              </div>
            ))}
          </nav>

          <div className="help-content">
            {groups.map((g) => (
              <div key={g.group}>
                <h2 className="help-group-heading">{g.group}</h2>
                {g.items.map((s) => (
                  <article key={s.id} id={s.id} className="panel help-article">
                    <h3>
                      {s.title}
                      {s.audience === "admin" && <span className="help-badge">superadmin</span>}
                    </h3>
                    {s.body}
                  </article>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
