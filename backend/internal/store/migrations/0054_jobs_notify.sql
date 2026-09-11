-- 0054: NOTIFY on job insert.
--
-- Push-not-pull dispatch (design doc v3 S6.1 / build plan for USP job
-- dispatch): cmd/uspc needs a way to learn about a newly queued job
-- without polling. This trigger fires pg_notify('acs_jobs_queued', ...)
-- with the job's device_id (as text) whenever a row lands in the jobs
-- table already QUEUED.
--
-- INSERT only, deliberately: a job re-queued via Repository.Requeue
-- (existing method, an UPDATE that sets status back to QUEUED on a
-- retry) does NOT get a fresh notify from this trigger -- WHEN
-- (NEW.status = 'QUEUED') only ever sees the row as it looks on INSERT.
-- That gap is intentional, not an oversight: the periodic sweep (build
-- plan Task 4) is the stated safety net both for a requeued job and for
-- any notification lost to a connection blip, so a future reader should
-- not assume requeue-triggers-notify.
--
-- The channel name 'acs_jobs_queued' must stay byte-for-byte identical
-- to jobs.NotifyChannel (internal/jobs/listen.go) -- a plain SQL file
-- can't reference a Go constant, so the two are kept in sync by hand.
-- internal/jobs/listen_test.go's TestListenReceivesNotification listens
-- on jobs.NotifyChannel and inserts a real job; if the two values ever
-- drift, that test times out waiting for a notification that never
-- arrives on the channel it's listening on.
CREATE FUNCTION notify_jobs_queued() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('acs_jobs_queued', NEW.device_id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER jobs_notify_queued
    AFTER INSERT ON jobs
    FOR EACH ROW
    WHEN (NEW.status = 'QUEUED')
    EXECUTE FUNCTION notify_jobs_queued();
