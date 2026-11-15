import { useEffect, useState, type ReactNode } from "react";
import { getAuthState, setAuthenticated, forgetToken, clearAuth, subscribe, type AuthState } from "./tokenStore";
import { api, cookieSessionWorks, isMFAChallenge, type Session } from "../api/client";
import { AuthContext } from "./context";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>(getAuthState());

  useEffect(() => subscribe(() => setState(getAuthState())), []);

  // A sign-in answers with the session twice: as a cookie the console
  // cannot read, and as a token in the body. The token is held in memory
  // only until the API confirms that the cookie reaches it; then it is
  // forgotten. If the cookie does not work (see tokenStore.ts) the token
  // stays, in memory, for the life of the page.
  const adopt = (username: string, session: Session) => {
    setAuthenticated(session.token, username, session.role);
    if (session.session_cookie) {
      void cookieSessionWorks().then((ok) => {
        if (ok) forgetToken();
      });
    }
  };

  // After a reload the console knows only that someone was signed in.
  // Ask the API who: the answer corrects the name and role, and a 401
  // (handled in api/client.ts) signs the console out.
  useEffect(() => {
    const s = getAuthState();
    if (!s.signedIn || s.token) return;
    api
      .session()
      .then((info) => {
        if (info.auth_required && info.username && info.role) setAuthenticated(null, info.username, info.role);
      })
      .catch(() => {
        // Unreachable API or a 401: the request helper has dealt with it.
      });
  }, []);

  const login = async (username: string, password: string) => {
    const resp = await api.login(username, password);
    if (isMFAChallenge(resp)) return resp;
    adopt(username, resp);
    return null;
  };

  const acceptSession = (username: string, session: Session) => adopt(username, session);

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

  return <AuthContext.Provider value={{ ...state, login, acceptSession, logout }}>{children}</AuthContext.Provider>;
}
