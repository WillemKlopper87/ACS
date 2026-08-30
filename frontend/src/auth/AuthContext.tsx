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

  return <AuthContext.Provider value={{ ...state, login, logout: clearAuth }}>{children}</AuthContext.Provider>;
}
