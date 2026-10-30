-- Hardening 2026-10-30 (handoff plan §3a.5): a job's payload holds the
-- plain secrets it must send -- SetParameterValues writes of a Wi-Fi
-- KeyPassphrase, ConnectionRequestPassword or XMPP Password, and the
-- Download/Upload "password" -- and was kept that way for the whole job
-- retention (90 days) after the RPC had gone out.
--
-- A trigger rather than Go code: about ten code paths finish a job
-- (MarkSuccess/MarkFailed/MarkTimeout, lease recovery, dropped-session
-- recovery, diagnostics timeouts...), and one rule here covers all of
-- them and any added later. It fires on the transition into SUCCESS,
-- FAILED or TIMEOUT; a job that is still QUEUED, RPC_SENT or awaiting a
-- result keeps the values it needs (SPV read-back verification compares
-- against them while the job is QUEUED). scheduled_jobs is untouched: the
-- schedule worker needs its values, and GET /schedules redacts on read.
--
-- What is redacted matches parameters.RedactPayloadSecrets: the "value" of
-- any object whose "name" is a secret parameter, and any string field whose
-- own key is secret-named. The name pattern is parameters.IsSecretParameter's,
-- as in 0085; keep the three in sync by hand.
CREATE FUNCTION redact_payload_secrets(doc jsonb) RETURNS jsonb AS $$
DECLARE
    pattern CONSTANT text := '(password|passphrase|secret|psk|presharedkey|privatekey)';
    secret_name boolean;
    out jsonb;
BEGIN
    CASE jsonb_typeof(doc)
    WHEN 'object' THEN
        secret_name := jsonb_typeof(doc->'name') = 'string' AND lower(doc->>'name') ~ pattern;
        SELECT COALESCE(jsonb_object_agg(e.key,
            CASE
                WHEN jsonb_typeof(e.value) = 'string'
                     AND (e.value #>> '{}') NOT IN ('', '***REDACTED***')
                     AND (lower(e.key) ~ pattern OR (e.key = 'value' AND secret_name))
                THEN '"***REDACTED***"'::jsonb
                ELSE redact_payload_secrets(e.value)
            END), '{}'::jsonb)
        INTO out FROM jsonb_each(doc) e;
        RETURN out;
    WHEN 'array' THEN
        SELECT COALESCE(jsonb_agg(redact_payload_secrets(a.value) ORDER BY a.ord), '[]'::jsonb)
        INTO out FROM jsonb_array_elements(doc) WITH ORDINALITY a(value, ord);
        RETURN out;
    ELSE
        RETURN doc;
    END CASE;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE FUNCTION redact_finished_job_secrets() RETURNS trigger AS $$
BEGIN
    NEW.payload := redact_payload_secrets(NEW.payload);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER jobs_redact_finished_secrets
    BEFORE UPDATE OF status ON jobs
    FOR EACH ROW
    WHEN (NEW.status IN ('SUCCESS', 'FAILED', 'TIMEOUT') AND OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION redact_finished_job_secrets();

-- Scrub jobs that already finished.
UPDATE jobs
SET payload = redact_payload_secrets(payload)
WHERE status IN ('SUCCESS', 'FAILED', 'TIMEOUT')
  AND lower(payload::text) ~ '(password|passphrase|secret|psk|presharedkey|privatekey)';
