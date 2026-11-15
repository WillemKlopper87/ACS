import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, type MFAEnrollment, type MFAStatus } from "../api/client";
import { MfaCodeInput, MfaSetup, RecoveryCodes } from "../components/Mfa";
import { useAuth } from "../auth/useAuth";
import { toast } from "../lib/toast";

type Mode = "idle" | "setup" | "disable" | "regenerate";

// The signed-in operator's own account: who they are, and their second
// sign-in step. Everything here acts on the caller only.
export function Account() {
  const { username, role } = useAuth();
  const [status, setStatus] = useState<MFAStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("idle");
  const [enrollment, setEnrollment] = useState<MFAEnrollment | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  const load = useCallback(async () => {
    try {
      setStatus(await api.getMFA());
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not read your account");
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const reset = () => {
    setMode("idle");
    setEnrollment(null);
    setCode("");
  };

  const startSetup = async () => {
    setBusy(true);
    try {
      setEnrollment(await api.enrollMFA());
      setMode("setup");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Could not start set-up", "error");
    } finally {
      setBusy(false);
    }
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (mode === "setup") {
        setCodes((await api.confirmMFA(code)).recovery_codes);
        toast("Two-step sign-in is on", "success");
      } else if (mode === "regenerate") {
        setCodes((await api.regenerateRecoveryCodes(code)).recovery_codes);
      } else if (mode === "disable") {
        await api.disableMFA(code);
        toast("Two-step sign-in is off", "success");
      }
      reset();
      await load();
    } catch (err) {
      setCode("");
      toast(err instanceof ApiError ? err.message : "Could not check the code", "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={load}>Retry</button>
        </div>
      )}

      <div className="panel account-panel">
        <h3>Your account</h3>
        <dl className="kv">
          <dt>Username</dt>
          <dd>{username ?? "—"}</dd>
          <dt>Role</dt>
          <dd>{role ?? "—"}</dd>
        </dl>
      </div>

      <div className="panel account-panel">
        <h3>
          Two-step sign-in{" "}
          {status && <span className={`pill ${status.enabled ? "pill-ok" : "pill-neutral"}`}>{status.enabled ? "on" : "off"}</span>}
        </h3>

        {codes ? (
          <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />
        ) : !status ? (
          !error && <p className="dim">Loading…</p>
        ) : (
          <>
            <p className="dim account-lede">
              {status.enabled
                ? "Signing in asks for your password and then a code from your authenticator app."
                : "Add a second step to your sign-in: after your password, a six-digit code from an authenticator app on your phone."}
              {status.required && " Your role requires it."}
            </p>

            {mode === "idle" && !status.enabled && (
              <>
                {!status.available && (
                  <div className="banner info">
                    This server has no encryption key for storing sign-in secrets, so two-step sign-in cannot be set up. Ask an administrator to set
                    ACS_CREDENTIAL_ENCRYPTION_KEY.
                  </div>
                )}
                <button className="btn primary" onClick={startSetup} disabled={busy || !status.available}>
                  Set up two-step sign-in
                </button>
              </>
            )}

            {mode === "idle" && status.enabled && (
              <>
                <dl className="kv">
                  <dt>Recovery codes left</dt>
                  <dd>
                    {status.recovery_codes_left}
                    {status.recovery_codes_left <= 2 && " — make new ones soon"}
                  </dd>
                </dl>
                <div className="form-row">
                  <button className="btn" onClick={() => setMode("regenerate")}>
                    New recovery codes
                  </button>
                  <button
                    className="btn danger"
                    onClick={() => setMode("disable")}
                    disabled={status.required}
                    title={status.required ? "Your role requires two-step sign-in" : undefined}
                  >
                    Turn off
                  </button>
                </div>
              </>
            )}

            {mode !== "idle" && (
              <form onSubmit={onSubmit} className="account-form">
                {mode === "setup" && enrollment && <MfaSetup uri={enrollment.otpauth_uri} secret={enrollment.secret} />}
                {mode === "regenerate" && <p className="dim">Enter a current code to replace your recovery codes. The old ones stop working.</p>}
                {mode === "disable" && <p className="dim">Enter a current code to turn two-step sign-in off.</p>}
                <MfaCodeInput value={code} onChange={setCode} label="Code from your app" />
                <div className="form-row">
                  <button type="submit" className={`btn ${mode === "disable" ? "danger" : "primary"}`} disabled={busy || !code.trim()}>
                    {busy ? "Checking…" : mode === "setup" ? "Turn on" : mode === "disable" ? "Turn off" : "Make new codes"}
                  </button>
                  <button type="button" className="btn" onClick={reset}>
                    Cancel
                  </button>
                </div>
              </form>
            )}
          </>
        )}
      </div>
    </section>
  );
}
