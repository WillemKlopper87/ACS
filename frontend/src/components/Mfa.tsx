import qrcode from "qrcode-generator";
import { useMemo, useState } from "react";
import { groupSecret } from "../lib/mfa";
import { toast } from "../lib/toast";

// The QR an authenticator app scans. Drawn as SVG rectangles from the
// module matrix, so nothing is injected as markup. Always dark on white:
// phone cameras read that reliably whatever the console theme is.
export function MfaQr({ uri }: { uri: string }) {
  const { size, path } = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(uri);
    qr.make();
    const n = qr.getModuleCount();
    let d = "";
    for (let row = 0; row < n; row++) {
      for (let col = 0; col < n; col++) {
        if (qr.isDark(row, col)) d += `M${col} ${row}h1v1h-1z`;
      }
    }
    return { size: n, path: d };
  }, [uri]);
  const quiet = 4; // the blank margin the QR standard asks for
  return (
    <svg
      className="mfa-qr"
      role="img"
      aria-label="QR code to scan with an authenticator app"
      viewBox={`${-quiet} ${-quiet} ${size + 2 * quiet} ${size + 2 * quiet}`}
      shapeRendering="crispEdges"
    >
      <rect x={-quiet} y={-quiet} width={size + 2 * quiet} height={size + 2 * quiet} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}

// Set-up instructions with the QR and the typed alternative. The code
// field and its button belong to the caller, which knows what confirming
// should do (sign in, or just turn MFA on).
export function MfaSetup({ uri, secret }: { uri: string; secret: string }) {
  return (
    <div className="mfa-setup">
      <MfaQr uri={uri} />
      <ol className="mfa-steps">
        <li>Open an authenticator app (Google Authenticator, Microsoft Authenticator, 1Password, Authy…).</li>
        <li>
          Scan the code, or add an account by hand with this key:
          <code className="mfa-secret">{groupSecret(secret)}</code>
        </li>
        <li>Enter the six-digit code the app shows.</li>
      </ol>
    </div>
  );
}

// A one-time code field: numeric keypad on phones, and the browser may
// offer the code from an authenticator or a text message.
export function MfaCodeInput({ value, onChange, label = "Code", autoFocus = true }: { value: string; onChange: (v: string) => void; label?: string; autoFocus?: boolean }) {
  return (
    <label className="field">
      <span>{label}</span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        inputMode="text"
        autoComplete="one-time-code"
        autoCapitalize="characters"
        spellCheck={false}
        autoFocus={autoFocus}
        maxLength={16}
      />
    </label>
  );
}

// Recovery codes are shown once. The operator must say they have kept
// them before moving on, because nothing can show them again.
export function RecoveryCodes({ codes, onDone, doneLabel = "I have saved these codes" }: { codes: string[]; onDone: () => void; doneLabel?: string }) {
  const [copied, setCopied] = useState(false);
  const text = codes.join("\n");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      toast("Could not copy — select the codes and copy them by hand", "error");
    }
  };
  const download = () => {
    const url = URL.createObjectURL(new Blob([`ACS console recovery codes\nEach code works once.\n\n${text}\n`], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "acs-recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <div className="mfa-recovery">
      <p className="auth-note">
        <strong>Save these recovery codes.</strong> Each one signs you in once if you lose your phone. They are not shown again.
      </p>
      <ul className="mfa-codes">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="form-row">
        <button type="button" className="btn" onClick={copy}>
          {copied ? "Copied" : "Copy"}
        </button>
        <button type="button" className="btn" onClick={download}>
          Download
        </button>
      </div>
      <button type="button" className="btn primary" onClick={onDone}>
        {doneLabel}
      </button>
    </div>
  );
}
