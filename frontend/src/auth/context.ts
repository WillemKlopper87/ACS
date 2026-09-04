import { createContext } from "react";
import type { AuthState } from "./tokenStore";

export interface AuthContextValue extends AuthState {
  login: (username: string, password: string) => Promise<void>;
  // Async because it revokes the session server-side before clearing
  // local state; callers that don't care about completion can ignore it.
  logout: () => Promise<void>;
}

// The context object lives apart from the provider component so
// AuthContext.tsx exports only a component (React Fast Refresh).
export const AuthContext = createContext<AuthContextValue | null>(null);
