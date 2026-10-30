// Vendor family grouping for display only. The stored manufacturer string is
// what the CPE reports in its Inform and is never rewritten: a Nokia WiFi
// Beacon 3.1 reports "ALCL" (Alcatel-Lucent, Nokia's legacy OUI owner) while
// the FastMile gateway it sits behind reports "Nokia". Operators expect both
// under one vendor in filters, breakdowns and vendor hints.

export type VendorFamily = "Huawei" | "Nokia" | "Other";

const NOKIA_MANUFACTURERS = /^(nokia|alcl|alcatel[\s-]?lucent|alu)\b/;

function norm(value: string | null | undefined): string {
  return (value ?? "").trim().toLowerCase();
}

/** Classifies a device into a vendor family from manufacturer and model. */
export function vendorFamilyOf(manufacturer: string | null | undefined, model?: string | null): VendorFamily {
  const m = norm(manufacturer);
  const p = norm(model);
  if (m.includes("huawei") || p.includes("huawei")) return "Huawei";
  if (NOKIA_MANUFACTURERS.test(m) || m.includes("nokia") || p.includes("nokia") || p.includes("fastmile")) return "Nokia";
  return "Other";
}

/**
 * The name devices are grouped under: the family for known families, the
 * reported manufacturer (trimmed) otherwise.
 */
export function vendorGroup(manufacturer: string | null | undefined): string {
  const family = vendorFamilyOf(manufacturer);
  if (family !== "Other") return family;
  const raw = (manufacturer ?? "").trim();
  return raw || "Unknown";
}

/**
 * Display label that keeps the reported string visible when it differs from
 * the family name, e.g. "Nokia (ALCL)".
 */
export function vendorLabel(manufacturer: string | null | undefined): string {
  const group = vendorGroup(manufacturer);
  const raw = (manufacturer ?? "").trim();
  return raw && raw.toLowerCase() !== group.toLowerCase() ? `${group} (${raw})` : group;
}

/** Merges per-manufacturer counts into per-vendor-family counts. */
export function groupCountsByVendor(counts: Record<string, number> | null | undefined): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [manufacturer, n] of Object.entries(counts ?? {})) {
    const key = vendorGroup(manufacturer);
    out[key] = (out[key] ?? 0) + n;
  }
  return out;
}
