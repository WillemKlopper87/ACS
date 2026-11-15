// Alert policy durations arrive from the API as nanoseconds (a Go
// time.Duration). The console showed the raw number; these turn it into
// something an operator can read.

export function policyThresholdSeconds(v: string | number | undefined): number {
  const n = typeof v === "string" ? Number(v) : (v ?? 0);
  return Number.isFinite(n) ? Math.round(n / 1e9) : 0;
}

export function fmtSeconds(total: number): string {
  if (total <= 0) return "—";
  if (total < 60) return `${total}s`;
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return [h ? `${h}h` : "", m ? `${m}m` : "", s ? `${s}s` : ""].filter(Boolean).join(" ");
}
