// Module-level auth state, outside React — api/client.ts's plain
// `request()` helper needs to read it on every call without being a
// component itself, and needs to be able to clear it on a 401 without
// going through a context dispatch. AuthContext.tsx is a thin React
// wrapper around this for components that want to render based on it.
//
// Where the session lives. The session itself is a cookie the API sets at
// sign-in and that no script can read, this one included (HttpOnly). What
// is kept here, and persisted, is only what the console needs to draw
// itself: that someone is signed in, who, and with which role. Nothing in
// browser storage can be used to act as the operator.
//
// `token` is the exception and is never persisted. It holds the session
// as a bearer token, in memory only, when the API could not give the
// console a cookie (ACS_API_CORS_ORIGIN="*", the lab's same-origin web-GUI
// mode, or an API on another site than the console). Such a session ends
// when the page is reloaded.
const STORAGE_KEY = "acs_auth";

export interface AuthState {
  // Someone is signed in, as far as the console knows. The API has the
  // last word: a 401 clears it.
  signedIn: boolean;
  // In memory only; null when the session is the cookie. See above.
  token: string | null;
  username: string | null;
  role: string | null;
  // True once we've learned the backend has operator auth enabled
  // (ACS_JWT_SIGNING_SECRET configured) — either a request came back 401,
  // or a login previously succeeded. Persisted, deliberately: if this
  // reset to false on every page load, a *valid* session would never 401
  // (it's valid), so this would never get set again — and signing out
  // would leave the app rendering as if no login were required at all,
  // since the gate depends on this flag. Once true, stays true until
  // proven otherwise is out of scope here (an operator disabling auth
  // entirely after a client already learned it was required is treated
  // as a rare enough edge case not to build a "forget this" path for).
  authRequired: boolean;
}

const SIGNED_OUT: AuthState = { signedIn: false, token: null, username: null, role: null, authRequired: false };

function loadPersisted(): AuthState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...SIGNED_OUT };
    const stored = JSON.parse(raw) as Record<string, unknown>;
    // A console from before the cookie kept the token here. It is dropped,
    // and removed from storage, the first time this version loads: that
    // operator signs in once more.
    const hadToken = typeof stored.token === "string" && stored.token !== "";
    const next: AuthState = {
      signedIn: stored.signedIn === true && !hadToken,
      token: null,
      username: typeof stored.username === "string" && !hadToken ? stored.username : null,
      role: typeof stored.role === "string" && !hadToken ? stored.role : null,
      authRequired: stored.authRequired === true || hadToken,
    };
    if (hadToken) write(next);
    return next;
  } catch {
    return { ...SIGNED_OUT };
  }
}

function write(s: AuthState) {
  const { signedIn, username, role, authRequired } = s;
  if (signedIn || authRequired) {
    // Never the token.
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ signedIn, username, role, authRequired }));
  } else {
    localStorage.removeItem(STORAGE_KEY);
  }
}

let state: AuthState = loadPersisted();
const listeners = new Set<() => void>();

function persist() {
  write(state);
}

function notify() {
  listeners.forEach((l) => l());
}

export function getAuthState(): AuthState {
  return state;
}

// token: the session as a bearer token, held in memory until the console
// has confirmed that the cookie works (forgetToken), or for the
// whole page life when it does not. Pass null when there is none.
export function setAuthenticated(token: string | null, username: string, role: string) {
  state = { signedIn: true, token, username, role, authRequired: true };
  persist();
  notify();
}

// The API confirmed the cookie carries the session: the in-memory token
// is no longer needed and is forgotten.
export function forgetToken() {
  if (state.token === null) return;
  state = { ...state, token: null };
  notify();
}

export function clearAuth() {
  state = { signedIn: false, token: null, username: null, role: null, authRequired: state.authRequired };
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
