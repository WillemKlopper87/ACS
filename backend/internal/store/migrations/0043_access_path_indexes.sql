-- Audit P2.2: index every foreign key and the status/time access paths
-- the workers, reaper, retention pass, and dashboards actually query.
-- All IF NOT EXISTS so this is safe on databases where an operator has
-- already added some of these by hand.
CREATE INDEX IF NOT EXISTS device_credentials_device_id_idx ON device_credentials (device_id);
CREATE INDEX IF NOT EXISTS device_parameter_names_device_id_idx ON device_parameter_names (device_id);
CREATE INDEX IF NOT EXISTS device_projects_project_id_idx ON device_projects (project_id);
CREATE INDEX IF NOT EXISTS device_vpn_peers_device_id_idx ON device_vpn_peers (device_id);
CREATE INDEX IF NOT EXISTS device_webgui_config_device_id_idx ON device_webgui_config (device_id);
CREATE INDEX IF NOT EXISTS firmware_rollout_image_idx ON firmware_rollout (firmware_image_id);
CREATE INDEX IF NOT EXISTS firmware_rollout_rollback_image_idx ON firmware_rollout (rollback_firmware_image_id);
CREATE INDEX IF NOT EXISTS firmware_rollout_device_device_idx ON firmware_rollout_device (device_id);
CREATE INDEX IF NOT EXISTS firmware_rollout_device_job_idx ON firmware_rollout_device (job_id);
CREATE INDEX IF NOT EXISTS password_reset_tokens_operator_idx ON password_reset_tokens (operator_id);
CREATE INDEX IF NOT EXISTS password_reset_tokens_expires_idx ON password_reset_tokens (expires_at);
CREATE INDEX IF NOT EXISTS webhook_deliveries_subscription_idx ON webhook_deliveries (subscription_id);
CREATE INDEX IF NOT EXISTS webhook_deliveries_status_created_idx ON webhook_deliveries (status, created_at);
CREATE INDEX IF NOT EXISTS cwmp_sessions_current_job_idx ON cwmp_sessions (current_job_id);
CREATE INDEX IF NOT EXISTS cwmp_sessions_closed_at_idx ON cwmp_sessions (closed_at);
CREATE INDEX IF NOT EXISTS jobs_status_created_idx ON jobs (status, created_at);
CREATE INDEX IF NOT EXISTS jobs_status_completed_idx ON jobs (status, completed_at);
CREATE INDEX IF NOT EXISTS parameter_history_recorded_at_idx ON parameter_history (recorded_at);
CREATE INDEX IF NOT EXISTS uploaded_files_status_created_idx ON uploaded_files (status, created_at);
CREATE INDEX IF NOT EXISTS audit_log_device_occurred_idx ON audit_log (device_id, occurred_at DESC);
