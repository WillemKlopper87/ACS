-- Phase 6 (build plan §4 Phase 6 / design doc v3 §11.3-11.4, credential
-- class 4: "REST/API operator ... OIDC/JWT rotation"). No external IdP
-- exists in this lab, so cmd/api is its own minimal token issuer:
-- operators are rows here, passwords are bcrypt hashes (never plaintext,
-- never logged — v3 §11.7's "do not store sensitive values in normal
-- tables" is honored by hashing, not by moving this to a secrets
-- manager, which is out of scope for this pass), and login exchanges
-- them for a short-lived JWT.
CREATE TABLE operators (
    id UUID PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'readonly')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
