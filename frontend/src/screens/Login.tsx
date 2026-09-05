import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/useAuth";
import { api, ApiError } from "../api/client";

function AuthCard({ children }: { children: React.ReactNode }) {
  return (
    <main aria-labelledby="auth-heading" className="auth-shell">
      <div className="panel auth-card">
        <h1 id="auth-heading">
          <span style={{ color: "var(--accent)" }}>●</span> ACS<span style={{ color: "var(--ink-faint)" }}>/</span>console
        </h1>
        {children}
      </div>
    </main>
  );
}

// The emailed reset link is just "<frontend>/?token=..." (see cmd/api's
// ACS_FRONTEND_BASE_URL) — a query param on whatever path the console is
// served from, not a route of its own. So this component reads it
// directly rather than adding a route for one page. (The app does use
// react-router elsewhere; it just isn't involved here.)
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
        <p className="auth-note">
          Password reset. <a href={window.location.pathname}>Sign in</a> with your new password.
        </p>
      </AuthCard>
    );
  }

  return (
    <AuthCard>
      <form onSubmit={onSubmit}>
        <p className="auth-note">Choose a new password.</p>
        <label className="field">
          <span>New password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus autoComplete="new-password" />
        </label>
        <label className="field">
          <span>Confirm password</span>
          <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" />
        </label>
        {error && <div className="banner error" style={{ marginBottom: 0 }}>{error}</div>}
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
        <p className="auth-note">
          If an account with that username has an email on file, a reset link has been sent. It expires in 4 hours.
        </p>
        <button className="btn" onClick={onBack}>Back to sign in</button>
      </AuthCard>
    );
  }

  return (
    <AuthCard>
      <form onSubmit={onSubmit}>
        <p className="auth-note">Enter your username to receive a reset link by email.</p>
        <label className="field">
          <span>Username</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus autoComplete="username" />
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
      <form onSubmit={onSubmit}>
        <p className="auth-note">Sign in to continue.</p>

        <label className="field">
          <span>Username</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus autoComplete="username" />
        </label>

        <label className="field">
          <span>Password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
        </label>

        {error && <div className="banner error" style={{ marginBottom: 0 }}>{error}</div>}

        <button type="submit" className="btn primary" disabled={submitting || !username || !password}>
          {submitting ? "Signing in…" : "Sign in"}
        </button>
        <button type="button" className="btn" onClick={() => setForgotPassword(true)}>
          Forgot password?
        </button>
      </form>
    </AuthCard>
  );
}
