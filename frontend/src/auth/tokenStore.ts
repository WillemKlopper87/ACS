// Module-level auth state, outside React — api/client.ts's plain
// `request()` helper needs to read the current token on every call
// without being a component itself, and needs to be able to clear it on
// a 401 without going through a context dispatch. AuthContext.tsx is a
// thin React wrapper around this for components that want to render
// based on it.
const STORAGE_KEY = "acs_auth";

export interface AuthState {
  token: string | null;
  username: string | null;
  role: string | null;
  // True once we've learned the backend has operator auth enabled
  // (ACS_JWT_SIGNING_SECRET configured) — either a request came back 401,
  // or a login previously succeeded. Persisted, deliberately: if this
  // reset to false on every page load, a *valid* persisted token would
  // never 401 (it's valid), so this would never get set again this
  // session — and signing out would clear the token but leave the app
  // rendering as if no login were required at all, since the gate below
  // depends on this flag. Once true, stays true until proven otherwise
  // is out of scope here (an operator disabling auth entirely after a
  // client already learned it was required is treated as a rare enough
  // edge case not to build a "forget this" path for).
  authRequired: boolean;
}

function loadPersisted(): AuthState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { token: null, username: null, role: null, authRequired: false };
    return { authRequired: false, ...JSON.parse(raw) };
  } catch {
    return { token: null, username: null, role: null, authRequired: false };
  }
}

let state: AuthState = loadPersisted();
const listeners = new Set<() => void>();

function persist() {
  const { token, username, role, authRequired } = state;
  if (token || authRequired) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ token, username, role, authRequired }));
  } else {
    localStorage.removeItem(STORAGE_KEY);
  }
}

function notify() {
  listeners.forEach((l) => l());
}

export function getAuthState(): AuthState {
  return state;
}

export function setAuthenticated(token: string, username: string, role: string) {
  state = { token, username, role, authRequired: true };
  persist();
  notify();
}

export function clearAuth() {
  state = { token: null, username: null, role: null, authRequired: state.authRequired };
  persist();
  notify();
}

export function markAuthRequired() {
  if (state.authRequired) return;
  state = { ...state, authRequired: true };
  persist();
  notify();
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
