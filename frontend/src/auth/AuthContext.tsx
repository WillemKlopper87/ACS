import { useEffect, useState, type ReactNode } from "react";
import { getAuthState, setAuthenticated, clearAuth, subscribe, type AuthState } from "./tokenStore";
import { api } from "../api/client";
import { AuthContext } from "./context";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>(getAuthState());

  useEffect(() => subscribe(() => setState(getAuthState())), []);

  const login = async (username: string, password: string) => {
    const resp = await api.login(username, password);
    setAuthenticated(resp.token, username, resp.role);
  };

  // Revoke server-side first, then clear locally. The local clear runs
  // either way: if the revoke call fails (API down, token already
  // expired) the operator still gets signed out of this browser rather
  // than being trapped in a session they asked to end. Best-effort
  // revocation is strictly better than the previous local-only clear,
  // which left a captured token valid until its own expiry.
  const logout = async () => {
    try {
      await api.logout();
    } catch {
      // Deliberately ignored — see above.
    } finally {
      clearAuth();
    }
  };

  return <AuthContext.Provider value={{ ...state, login, logout }}>{children}</AuthContext.Provider>;
}
