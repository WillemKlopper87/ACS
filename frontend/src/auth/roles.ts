// Mirrors internal/operators' readonly < noc < manager < superadmin
// hierarchy (admin-platform backlog: RBAC tier expansion, migration 0032)
// so the frontend can gray out write actions a readonly operator would
// just get a 403 from — the backend remains the real enforcement boundary
// (both the rank check AND, for the curated capability list, the
// superadmin-configurable permission matrix), this is purely UX.
const RANK: Record<string, number> = { readonly: 0, noc: 1, manager: 2, superadmin: 3 };

// canWrite reports whether role clears the "noc" bar. A null role means
// auth is disabled (lab mode, ACS_JWT_SIGNING_SECRET unset) — every action
// is open then, same as it's always been.
export function canWrite(role: string | null): boolean {
  if (role === null) return true;
  return (RANK[role] ?? -1) >= RANK.noc;
}

export function canAdmin(role: string | null): boolean {
  if (role === null) return true;
  return (RANK[role] ?? -1) >= RANK.superadmin;
}
