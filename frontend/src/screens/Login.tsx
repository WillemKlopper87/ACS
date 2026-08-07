import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { api, ApiError } from "../api/client";

const fieldStyle = {
  background: "var(--surface-2)",
  border: "1px solid var(--border)",
  borderRadius: 6,
  padding: "0.5rem 0.6rem",
  color: "var(--ink)",
  fontSize: "0.88rem",
} as const;

function AuthCard({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center", background: "var(--bg)" }}>
      <div className="panel" style={{ width: "22rem", display: "flex", flexDirection: "column", gap: "0.9rem" }}>
        <div style={{ display: "flex", alignItems: "baseline", gap: "0.5rem", fontFamily: "var(--font-mono)", fontWeight: 600 }}>
          <span style={{ color: "var(--accent)" }}>●</span> ACS<span style={{ color: "var(--ink-faint)" }}>/</span>console
        </div>
        {children}
      </div>
    </div>
  );
}

// No router in this app — the emailed reset link is just
// "<frontend>/?token=..." (see cmd/api's ACS_FRONTEND_BASE_URL), and this
// component checks for that query param itself rather than pulling in a
// routing dependency for one page.
function ResetPasswordForm({ token }: { token: string }) {
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (password !== confirm) {
      setError("Passwords do not match");
      return;
    }
    if (password.length < 8) {
      setError("Password must be at least 8 characters");
      return;
    }
    setSubmitting(true);
    try {
      await api.confirmPasswordReset(token, password);
      setDone(true);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Reset failed");
    } finally {
      setSubmitting(false);
    }
  };

  if (done) {
    return (
      <AuthCard>
        <p style={{ margin: 0, color: "var(--ink-dim)", fontSize: "0.85rem" }}>
          Password reset. <a href={window.location.pathname}>Sign in</a> with your new password.
        </p>
      </AuthCard>
    );
  }

  return (
    <AuthCard>
      <form onSubmit={onSubmit} style={{ display: "flex", flexDirection: "column", gap: "0.9rem" }}>
        <p style={{ margin: 0, color: "var(--ink-dim)", fontSize: "0.85rem" }}>Choose a new password.</p>
        <label style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
          <span style={{ fontSize: "0.75rem", color: "var(--ink-dim)" }}>New password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus autoComplete="new-password" style={fieldStyle} />
        </label>
        <label style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
          <span style={{ fontSize: "0.75rem", color: "var(--ink-dim)" }}>Confirm password</span>
          <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" style={fieldStyle} />
        </label>
        {error && (
          <div style={{ color: "var(--danger)", fontSize: "0.82rem", background: "var(--danger-bg)", borderRadius: 6, padding: "0.5rem 0.6rem" }}>
            {error}
          </div>
        )}
        <button type="submit" className="btn primary" disabled={submitting || !password || !confirm}>
          {submitting ? "Resetting…" : "Reset password"}
        </button>
      </form>
    </AuthCard>
  );
}

function ForgotPasswordForm({ onBack }: { onBack: () => void }) {
  const [username, setUsername] = useState("");
  const [sent, setSent] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      await api.requestPasswordReset(username);
    } finally {
      setSubmitting(false);
      // Always show the same "check your email" outcome — the API itself
      // never reveals whether the username existed (no enumeration), so
      // the UI shouldn't either.
      setSent(true);
    }
  };

  if (sent) {
    return (
      <AuthCard>
        <p style={{ margin: 0, color: "var(--ink-dim)", fontSize: "0.85rem" }}>
          If an account with that username has an email on file, a reset link has been sent. It expires in 4 hours.
        </p>
        <button className="btn" onClick={onBack}>Back to sign in</button>
      </AuthCard>
    );
  }

  return (
    <AuthCard>
      <form onSubmit={onSubmit} style={{ display: "flex", flexDirection: "column", gap: "0.9rem" }}>
        <p style={{ margin: 0, color: "var(--ink-dim)", fontSize: "0.85rem" }}>Enter your username to receive a reset link by email.</p>
        <label style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
          <span style={{ fontSize: "0.75rem", color: "var(--ink-dim)" }}>Username</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus autoComplete="username" style={fieldStyle} />
        </label>
        <button type="submit" className="btn primary" disabled={submitting || !username}>
          {submitting ? "Sending…" : "Send reset link"}
        </button>
        <button type="button" className="btn" onClick={onBack}>Back to sign in</button>
      </form>
    </AuthCard>
  );
}

export function Login() {
  const { login } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [forgotPassword, setForgotPassword] = useState(false);

  const resetToken = new URLSearchParams(window.location.search).get("token");
  if (resetToken) return <ResetPasswordForm token={resetToken} />;
  if (forgotPassword) return <ForgotPasswordForm onBack={() => setForgotPassword(false)} />;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await login(username, password);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AuthCard>
      <form onSubmit={onSubmit} style={{ display: "flex", flexDirection: "column", gap: "0.9rem" }}>
        <p style={{ margin: 0, color: "var(--ink-dim)", fontSize: "0.85rem" }}>Sign in to continue.</p>

        <label style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
          <span style={{ fontSize: "0.75rem", color: "var(--ink-dim)" }}>Username</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus autoComplete="username" style={fieldStyle} />
        </label>

        <label style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
          <span style={{ fontSize: "0.75rem", color: "var(--ink-dim)" }}>Password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" style={fieldStyle} />
        </label>

        {error && (
          <div style={{ color: "var(--danger)", fontSize: "0.82rem", background: "var(--danger-bg)", borderRadius: 6, padding: "0.5rem 0.6rem" }}>
            {error}
          </div>
        )}

        <button type="submit" className="btn primary" disabled={submitting || !username || !password}>
          {submitting ? "Signing in…" : "Sign in"}
        </button>
        <button type="button" className="btn" style={{ fontSize: "0.78rem" }} onClick={() => setForgotPassword(true)}>
          Forgot password?
        </button>
      </form>
    </AuthCard>
  );
}
