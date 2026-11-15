import { createContext } from "react";
import type { AuthState } from "./tokenStore";
import type { MFAChallenge, Session } from "../api/client";

export interface AuthContextValue extends AuthState {
  // Resolves to null once signed in, or to the MFA challenge when the
  // password was right but a second step is still owed.
  login: (username: string, password: string) => Promise<MFAChallenge | null>;
  // Completes a sign-in whose session came from an MFA step.
  acceptSession: (username: string, session: Session) => void;
  // Async because it revokes the session server-side before clearing
  // local state; callers that don't care about completion can ignore it.
  logout: () => Promise<void>;
}

// The context object lives apart from the provider component so
// AuthContext.tsx exports only a component (React Fast Refresh).
export const AuthContext = createContext<AuthContextValue | null>(null);
