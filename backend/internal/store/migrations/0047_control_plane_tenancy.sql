-- Remediation P0.2-P0.7 (ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md
-- §5): device groups, configuration templates, continuous policies,
-- scheduled jobs, and firmware rollouts are control objects that act on
-- devices asynchronously, often long after the HTTP request that created
-- them. Before this migration none of them carried a tenant owner, so a
-- scoped operator's group/template/policy/schedule/rollout was effectively
-- platform-global -- it could target, and later act on, any customer's
-- devices regardless of who created it.
--
-- customer_id NULL means platform-global -- an explicit ownership state
-- restricted to superadmins and operators holding the GlobalAccess grant
-- (audit P0.1), never the default a scoped operator falls into by
-- omission. NOT a foreign key ON DELETE CASCADE: deleting a customer must
-- not silently delete another tenant's config; that stays an explicit
-- operator action against the now-orphaned objects.
ALTER TABLE device_groups ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX device_groups_customer_id_idx ON device_groups(customer_id);

ALTER TABLE config_templates ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX config_templates_customer_id_idx ON config_templates(customer_id);

ALTER TABLE policies ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX policies_customer_id_idx ON policies(customer_id);

ALTER TABLE scheduled_jobs ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX scheduled_jobs_customer_id_idx ON scheduled_jobs(customer_id);

ALTER TABLE firmware_rollout ADD COLUMN customer_id UUID REFERENCES customers(id);
CREATE INDEX firmware_rollout_customer_id_idx ON firmware_rollout(customer_id);

-- Unrelated one-hour landmine caught in the same pass (audit M-1): neither
-- FK below has an ON DELETE action, so the retention job's batch DELETE
-- FROM jobs fails with a foreign-key violation the moment any rollout job
-- or session's current job crosses the retention cutoff -- and since the
-- batch always re-selects the same oldest rows, it then fails every run,
-- forever, with jobs growing unbounded. A job being retention-pruned is
-- history; the rollout-device row and session row it's attached to should
-- simply forget which job that was, not block deletion.
ALTER TABLE firmware_rollout_device DROP CONSTRAINT firmware_rollout_device_job_id_fkey;
ALTER TABLE firmware_rollout_device ADD CONSTRAINT firmware_rollout_device_job_id_fkey
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE SET NULL;

ALTER TABLE cwmp_sessions DROP CONSTRAINT cwmp_sessions_current_job_id_fkey;
ALTER TABLE cwmp_sessions ADD CONSTRAINT cwmp_sessions_current_job_id_fkey
    FOREIGN KEY (current_job_id) REFERENCES jobs(id) ON DELETE SET NULL;
