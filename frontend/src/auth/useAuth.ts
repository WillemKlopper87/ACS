import { useContext } from "react";
import { AuthContext, type AuthContextValue } from "./AuthContext";

// Kept apart from AuthContext.tsx so that file exports only a component
// and React Fast Refresh can hot-swap it (lint: react/only-export-components).
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
