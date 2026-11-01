// One onboarding's way from sale to service, step by step, with the reason
// cmd/acs gave for the step it is on (docs/ONBOARDING.md).
import type { Onboarding } from "../api/client";
import { onboardingTimeline, STATE_LABEL, stateTone, type StepStatus } from "../lib/onboarding";
import { fmtTime, timeAgo } from "../lib/format";

const MARK: Record<StepStatus, string> = { done: "✓", current: "●", blocked: "!", pending: "○" };
const SPOKEN: Record<StepStatus, string> = { done: "done", current: "in progress", blocked: "stopped here", pending: "not yet" };

export function OnboardingTimeline({ onboarding }: { onboarding: Onboarding }) {
  const steps = onboardingTimeline(onboarding);
  return (
    <div className="ob-timeline-wrap">
      <div className="ob-state">
        <span className={`pill ${stateTone(onboarding.state)}`}>{STATE_LABEL[onboarding.state]}</span>
        {onboarding.state_reason && <span className="ob-reason">{onboarding.state_reason}</span>}
      </div>
      <ol className="ob-timeline">
        {steps.map((s) => (
          <li key={s.id} className={`ob-step ${s.status}`}>
            <span className="ob-mark" aria-hidden="true">{MARK[s.status]}</span>
            <span>{s.label}</span>
            <span className="sr-only"> — {SPOKEN[s.status]}</span>
          </li>
        ))}
      </ol>
      <p className="dim ob-times">
        Recorded <span title={`${fmtTime(onboarding.created_at)} UTC`}>{timeAgo(onboarding.created_at)}</span>
        {onboarding.contacted_at && (
          <>
            {" · "}first contact <span title={`${fmtTime(onboarding.contacted_at)} UTC`}>{timeAgo(onboarding.contacted_at)}</span>
          </>
        )}
        {onboarding.in_service_at && (
          <>
            {" · "}in service <span title={`${fmtTime(onboarding.in_service_at)} UTC`}>{timeAgo(onboarding.in_service_at)}</span>
          </>
        )}
      </p>
    </div>
  );
}
