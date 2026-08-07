export function timeAgo(iso?: string): string {
  if (!iso) return "—";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  return iso.replace("T", " ").replace("Z", "").slice(0, 19);
}

export function durationSeconds(createdAt: string, completedAt?: string): number | null {
  if (!completedAt) return null;
  return Math.round(((new Date(completedAt).getTime() - new Date(createdAt).getTime()) / 1000) * 10) / 10;
}
