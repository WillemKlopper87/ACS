-- The audit trail's retention, without giving the operator API's database
-- login the right to delete audit rows (security probe 2026-11-18, D2).
--
-- The API runs the retention pass. With DELETE on audit_log, someone who
-- took over the API could erase the record of what they had just done.
-- This function is the only way the API's login removes audit rows, and it
-- never removes one younger than 30 days, whatever cutoff it is given: the
-- recent trail outlives a compromise of the API. It runs with the owner's
-- rights (SECURITY DEFINER) and a fixed search_path; the API's login gets
-- EXECUTE on it and loses DELETE on the table (internal/store/dbroles).
CREATE OR REPLACE FUNCTION acs_prune_audit_log(p_cutoff timestamptz, p_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    youngest_allowed timestamptz := now() - interval '30 days';
    removed bigint;
BEGIN
    IF p_cutoff IS NULL OR p_cutoff > youngest_allowed THEN
        p_cutoff := youngest_allowed;
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 10000 THEN
        p_limit := 10000;
    END IF;
    DELETE FROM audit_log
     WHERE id IN (SELECT id FROM audit_log WHERE occurred_at < p_cutoff LIMIT p_limit);
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END
$$;

REVOKE ALL ON FUNCTION acs_prune_audit_log(timestamptz, integer) FROM PUBLIC;
