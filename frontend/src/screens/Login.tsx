import { useEffect, useState, type FormEvent } from "react";
import type { MFAChallenge, MFAEnrollment, Session } from "../api/client";
import { MfaCodeInput, MfaSetup, RecoveryCodes } from "../components/Mfa";
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

// The MFA token lasts five minutes. When it has run out the server says
// so with a 401 that is not about the code; send the operator back to the
// password step with that message instead of leaving them retyping codes.
function stepExpired(err: unknown): string | null {
  return err instanceof ApiError && err.status === 401 && /expired/i.test(err.message) ? err.message : null;
}

interface MfaStepProps {
  username: string;
  mfaToken: string;
  onRestart: (message?: string) => void;
}

// Second step for an operator with MFA on: a code from the app, or a
// recovery code.
function MfaVerifyStep({ username, mfaToken, onRestart }: MfaStepProps) {
  const { acceptSession } = useAuth();
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      acceptSession(username, await api.loginMFA(mfaToken, code));
    } catch (err) {
      const expired = stepExpired(err);
      if (expired) return onRestart(expired);
      setCode("");
      setError(err instanceof ApiError ? err.message : "Could not check the code");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AuthCard>
      <form onSubmit={onSubmit}>
        <p className="auth-note">
          Enter the six-digit code from your authenticator app for <strong>{username}</strong>. Lost your phone? Enter one of your recovery codes instead.
        </p>
        <MfaCodeInput value={code} onChange={setCode} />
        {error && <div className="banner error" style={{ marginBottom: 0 }}>{error}</div>}
        <button type="submit" className="btn primary" disabled={submitting || !code.trim()}>
          {submitting ? "Checking…" : "Verify"}
        </button>
        <button type="button" className="btn" onClick={() => onRestart()}>
          Back to sign in
        </button>
      </form>
    </AuthCard>
  );
}

// First sign-in for a role that must use MFA: set it up, prove it with a
// code, keep the recovery codes, then continue to the console.
function MfaEnrollStep({ username, mfaToken, onRestart }: MfaStepProps) {
  const { acceptSession } = useAuth();
  const [enrollment, setEnrollment] = useState<MFAEnrollment | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [session, setSession] = useState<(Session & { recovery_codes: string[] }) | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .loginMFAEnroll(mfaToken)
      .then((e) => !cancelled && setEnrollment(e))
      .catch((err) => {
        if (cancelled) return;
        const expired = stepExpired(err);
        if (expired) onRestart(expired);
        else setError(err instanceof ApiError ? err.message : "Could not start MFA set-up");
      });
    return () => {
      cancelled = true;
    };
    // onRestart is recreated each render; the token is what this depends on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mfaToken]);

  if (session) {
    return (
      <AuthCard>
        <RecoveryCodes codes={session.recovery_codes} onDone={() => acceptSession(username, session)} doneLabel="I have saved these codes — continue" />
      </AuthCard>
    );
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      setSession(await api.loginMFAConfirm(mfaToken, code));
    } catch (err) {
      const expired = stepExpired(err);
      if (expired) return onRestart(expired);
      setCode("");
      setError(err instanceof ApiError ? err.message : "Could not check the code");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AuthCard>
      <form onSubmit={onSubmit}>
        <p className="auth-note">
          Your account must use a second sign-in step. Set it up now for <strong>{username}</strong>.
        </p>
        {enrollment ? <MfaSetup uri={enrollment.otpauth_uri} secret={enrollment.secret} /> : !error && <p className="auth-note">Preparing…</p>}
        {enrollment && <MfaCodeInput value={code} onChange={setCode} autoFocus={false} />}
        {error && <div className="banner error" style={{ marginBottom: 0 }}>{error}</div>}
        <button type="submit" className="btn primary" disabled={submitting || !enrollment || !code.trim()}>
          {submitting ? "Checking…" : "Turn on and sign in"}
        </button>
        <button type="button" className="btn" onClick={() => onRestart()}>
          Back to sign in
        </button>
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
  const [challenge, setChallenge] = useState<MFAChallenge | null>(null);

  const resetToken = new URLSearchParams(window.location.search).get("token");
  if (resetToken) return <ResetPasswordForm token={resetToken} />;
  if (forgotPassword) return <ForgotPasswordForm onBack={() => setForgotPassword(false)} />;
  if (challenge) {
    const restart = (message?: string) => {
      setChallenge(null);
      setPassword("");
      setError(message ?? null);
    };
    return challenge.mfa === "enroll" ? (
      <MfaEnrollStep username={username} mfaToken={challenge.mfa_token} onRestart={restart} />
    ) : (
      <MfaVerifyStep username={username} mfaToken={challenge.mfa_token} onRestart={restart} />
    );
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      // A right password may still owe a second step.
      setChallenge(await login(username, password));
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
