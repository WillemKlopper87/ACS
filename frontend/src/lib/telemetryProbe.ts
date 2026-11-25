// The preset list of parameters worth reading live from a gateway (signal,
// link timers, location metadata), and checks for paths an operator types.

export const MAX_PROBE_PATHS = 60;

const ALWAYS: RegExp[] = [
  /^Device\.Cellular\.Interface\.\d+\.X_[A-Za-z0-9-]+_Attached(5G|Lte)CellStat\.(RSRPCurrent|RSRQCurrent|SNRCurrent|RSSICurrent|Band|PhysicalCellID|Cellid|DownlinkEarfcn|Downlink-NR-ARFCN)$/,
  /^Device\.(Cellular\.Interface|IP\.Interface|Ethernet\.Interface|Ethernet\.Link)\.\d+\.LastChange$/,
  /^Device\.DeviceInfo\.UpTime$/,
  /^Device\.Cellular\.Interface\.\d+\.NetworkInUse$/,
  /^Device\.Cellular\.RoamingStatus$/,
  /^Device\.DeviceInfo\.Location\.1\.(Source|AcquiredTime|X_[A-Za-z0-9-]+_TimeStamp)$/,
];

const COORDINATES = /^Device\.DeviceInfo\.Location\.1\.X_[A-Za-z0-9-]+_(Latitude|Longitude|Altitude)$/;

/** True for the location coordinate leaves (latitude, longitude, altitude). */
export function isCoordinatePath(path: string): boolean {
  return COORDINATES.test(path);
}

/** The preset, limited to parameters the device actually has. Location
 * coordinates are included only when asked for. */
export function probePaths(names: Record<string, boolean> | null, opts: { includeCoordinates: boolean }): string[] {
  if (!names) return [];
  const patterns = opts.includeCoordinates ? [...ALWAYS, COORDINATES] : ALWAYS;
  return Object.keys(names)
    .filter((n) => patterns.some((re) => re.test(n)))
    .sort((a, b) => a.localeCompare(b));
}

/** One path per line: trimmed, blanks dropped, duplicates removed, order kept. */
export function parsePaths(text: string): string[] {
  const seen = new Set<string>();
  for (const line of text.split(/\r?\n/)) {
    const p = line.trim();
    if (p) seen.add(p);
  }
  return [...seen];
}

export function checkPaths(
  paths: string[],
  names: Record<string, boolean> | null,
): { unknown: string[]; objectPaths: string[]; tooMany: boolean } {
  return {
    unknown: names ? paths.filter((p) => !Object.hasOwn(names, p)) : [],
    objectPaths: paths.filter((p) => p.endsWith(".")),
    tooMany: paths.length > MAX_PROBE_PATHS,
  };
}
