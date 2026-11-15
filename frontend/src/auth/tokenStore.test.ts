import { describe, it, expect, beforeEach, vi } from "vitest";

// tokenStore is a module-level singleton (deliberately, per its own doc
// comment — api/client.ts needs to read it outside of React). Each test
// needs a *fresh* module instance reading a controlled localStorage, so
// every test resets the module registry and re-imports rather than
// reusing one shared import across the file.
async function freshTokenStore() {
  vi.resetModules();
  return await import("./tokenStore");
}

beforeEach(() => {
  localStorage.clear();
});

describe("tokenStore", () => {
  it("starts unauthenticated with authRequired=false when nothing is persisted", async () => {
    const { getAuthState } = await freshTokenStore();
    const s = getAuthState();
    expect(s.signedIn).toBe(false);
    expect(s.token).toBeNull();
    expect(s.username).toBeNull();
    expect(s.role).toBeNull();
    expect(s.authRequired).toBe(false);
  });

  it("setAuthenticated signs in, holds the token in memory and marks authRequired", async () => {
    const { setAuthenticated, getAuthState } = await freshTokenStore();
    setAuthenticated("tok-123", "alice", "manager");
    const s = getAuthState();
    expect(s.signedIn).toBe(true);
    expect(s.token).toBe("tok-123");
    expect(s.username).toBe("alice");
    expect(s.role).toBe("manager");
    expect(s.authRequired).toBe(true);
  });

  // This is the exact bug caught during Playwright verification (build
  // plan §6's frontend login section): authRequired must survive
  // clearAuth(), or a sign-out leaves the app behaving as if login were
  // never required, since nothing would ever re-set the flag (a cleared
  // token can't 401 again to re-learn it).
  it("clearAuth removes the token but keeps authRequired learned", async () => {
    const mod = await freshTokenStore();
    mod.setAuthenticated("tok-123", "alice", "manager");
    mod.clearAuth();
    const s = mod.getAuthState();
    expect(s.signedIn).toBe(false);
    expect(s.token).toBeNull();
    expect(s.username).toBeNull();
    expect(s.role).toBeNull();
    expect(s.authRequired).toBe(true);
  });

  it("authRequired persists across a reload (a fresh module reading the same localStorage)", async () => {
    const mod1 = await freshTokenStore();
    mod1.markAuthRequired();

    const mod2 = await freshTokenStore();
    expect(mod2.getAuthState().authRequired).toBe(true);
  });

  // The session is a cookie no script can read (security assessment O6).
  // Nothing that can act as the operator may reach browser storage.
  it("never writes the token to browser storage", async () => {
    const mod = await freshTokenStore();
    mod.setAuthenticated("tok-123", "alice", "manager");
    const stored = localStorage.getItem("acs_auth") ?? "";
    expect(stored).not.toContain("tok-123");
    expect(JSON.parse(stored)).toEqual({ signedIn: true, username: "alice", role: "manager", authRequired: true });
    for (let i = 0; i < sessionStorage.length; i++) {
      expect(sessionStorage.getItem(sessionStorage.key(i) as string)).not.toContain("tok-123");
    }
  });

  it("a reload keeps who is signed in but not the token", async () => {
    const mod1 = await freshTokenStore();
    mod1.setAuthenticated("tok-123", "alice", "manager");

    const mod2 = await freshTokenStore();
    const s = mod2.getAuthState();
    expect(s.signedIn).toBe(true);
    expect(s.username).toBe("alice");
    expect(s.token).toBeNull();
    expect(s.authRequired).toBe(true);
  });

  it("forgetToken drops the in-memory token once the cookie is confirmed and stays signed in", async () => {
    const mod = await freshTokenStore();
    mod.setAuthenticated("tok-123", "alice", "manager");
    mod.forgetToken();
    const s = mod.getAuthState();
    expect(s.token).toBeNull();
    expect(s.signedIn).toBe(true);
  });

  it("a token stored by an earlier console version is removed and that operator signs in again", async () => {
    localStorage.setItem("acs_auth", JSON.stringify({ token: "old-token", username: "alice", role: "manager", authRequired: true }));
    const { getAuthState } = await freshTokenStore();
    const s = getAuthState();
    expect(s.signedIn).toBe(false);
    expect(s.token).toBeNull();
    expect(s.authRequired).toBe(true);
    expect(localStorage.getItem("acs_auth")).not.toContain("old-token");
  });

  it("markAuthRequired is idempotent once already true — no redundant notify", async () => {
    const mod = await freshTokenStore();
    let notifyCount = 0;
    const unsubscribe = mod.subscribe(() => notifyCount++);

    mod.markAuthRequired();
    expect(notifyCount).toBe(1);
    mod.markAuthRequired();
    expect(notifyCount).toBe(1);

    unsubscribe();
  });

  it("a corrupted localStorage value degrades to a safe default instead of throwing", async () => {
    localStorage.setItem("acs_auth", "{not valid json");
    const { getAuthState } = await freshTokenStore();
    const s = getAuthState();
    expect(s.token).toBeNull();
    expect(s.authRequired).toBe(false);
  });

  it("clearAuth with authRequired still false leaves nothing in localStorage (lab-mode default)", async () => {
    const mod = await freshTokenStore();
    mod.clearAuth();
    expect(localStorage.getItem("acs_auth")).toBeNull();
  });
});
