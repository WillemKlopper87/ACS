// Which jobs the console offers to cancel or retry. This mirrors
// internal/jobs/operator_actions.go so the buttons appear only where the
// action can work; the API remains the check, and it also refuses jobs
// that a rollout, an order, onboarding or the system owns.
const RETRYABLE_TYPES = new Set([
  "SET_PARAMETER",
  "GET_PARAMETER",
  "CONNECTION_REQUEST",
  "REBOOT",
  "SCHEDULE_INFORM",
  "DELETE_OBJECT",
  "SET_PARAMETER_ATTRIBUTES",
  "GET_PARAMETER_ATTRIBUTES",
  "DIAGNOSTICS_PING",
  "DIAGNOSTICS_TRACEROUTE",
]);

const ENDED_WITHOUT_SUCCESS = new Set(["FAILED", "TIMEOUT", "CANCELLED"]);

// Only a job nothing has been sent for can be withdrawn.
export function canCancelJob(status: string): boolean {
  return status === "QUEUED";
}

export function canRetryJob(job: { status: string; type: string }): boolean {
  return ENDED_WITHOUT_SUCCESS.has(job.status) && RETRYABLE_TYPES.has(job.type);
}
