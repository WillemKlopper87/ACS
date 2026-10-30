// Console helpers for the device diagnostics panel. The server is the
// authority (it answers 409 for an unsupported diagnostic); these only keep
// the panel from offering a test the device's discovered data model rules out.

export interface DiagnosticCapability {
  supported: boolean;
  path?: string;
  reason?: string;
}

// Why a diagnostic is unavailable, or null when it may be offered. An
// unknown capability (not loaded, read failed) is offered.
export function unsupportedReason(cap: DiagnosticCapability | null | undefined): string | null {
  if (!cap || cap.supported) return null;
  return cap.reason || "not supported by this device";
}

// Optional inputs the server did not send because discovery did not prove
// them writable (the CPE's own defaults apply).
export function skippedNote(skipped?: string[]): string {
  return skipped && skipped.length > 0 ? ` (device default used for ${skipped.join(", ")})` : "";
}
