-- Hardening 2026-10-30: secrets were cached in plain text. A value the ACS
-- wrote (Wi-Fi KeyPassphrase, ConnectionRequestPassword, XMPP Password...)
-- was cached from the SetParameterValues payload and returned by
-- GET /parameters to every role. parameters.Repository.Upsert now stores
-- ***REDACTED***; this scrubs what is already stored. The name pattern is
-- the same as parameters.IsSecretParameter.
UPDATE parameter_history
SET value = '***REDACTED***'
WHERE value <> '' AND value <> '***REDACTED***'
  AND lower(name) ~ '(password|passphrase|secret|psk|presharedkey|privatekey)';

UPDATE device_parameter_cache c
SET parameters = (
    SELECT jsonb_object_agg(
        e.key,
        CASE
            WHEN lower(e.key) ~ '(password|passphrase|secret|psk|presharedkey|privatekey)'
                 AND COALESCE(e.value->>'value', '') NOT IN ('', '***REDACTED***')
            THEN jsonb_set(e.value, '{value}', '"***REDACTED***"')
            ELSE e.value
        END)
    FROM jsonb_each(c.parameters) e
)
WHERE c.parameters <> '{}'::jsonb
  AND EXISTS (
    SELECT 1 FROM jsonb_each(c.parameters) e
    WHERE lower(e.key) ~ '(password|passphrase|secret|psk|presharedkey|privatekey)'
      AND COALESCE(e.value->>'value', '') NOT IN ('', '***REDACTED***'));
