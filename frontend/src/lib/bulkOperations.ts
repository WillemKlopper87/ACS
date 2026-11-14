import type { BulkOperation, BulkOperationItem } from "../api/types";

// What an item shows as its outcome: its job's status, NOT_QUEUED when no
// job could be created for the device, UNKNOWN when the job it had has
// since been pruned.
export function itemStatus(it: BulkOperationItem): string {
  if (it.job_status) return it.job_status;
  return it.command_key ? "UNKNOWN" : "NOT_QUEUED";
}

// Failures first, then what is still moving, then what is done.
const STATUS_ORDER: Record<string, number> = { FAILED: 0, TIMEOUT: 0, NOT_QUEUED: 1, CANCELLED: 2, QUEUED: 3, SUCCESS: 5 };

export function sortItems(items: BulkOperationItem[]): BulkOperationItem[] {
  return [...items].sort((a, b) => (STATUS_ORDER[itemStatus(a)] ?? 4) - (STATUS_ORDER[itemStatus(b)] ?? 4));
}

export function progressText(op: BulkOperation): string {
  const done = op.succeeded + op.failed + op.cancelled + op.not_queued + op.unknown;
  return `${done} of ${op.total} finished`;
}
