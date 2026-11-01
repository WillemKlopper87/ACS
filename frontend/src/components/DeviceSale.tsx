// The sale behind one device, on its Ownership sub-page: the onboarding
// timeline, or a way to claim the device when nobody sold it.
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type Onboarding } from "../api/client";
import type { Device } from "../api/types";
import { canManage } from "../auth/roles";
import { useAuth } from "../auth/useAuth";
import { onboardingTab } from "../lib/onboarding";
import { toast } from "../lib/toast";
import { OnboardingTimeline } from "./OnboardingTimeline";
import { OnboardingWizard } from "./OnboardingWizard";

export function DeviceSale({ device }: { device: Device }) {
  const { role } = useAuth();
  const [latest, setLatest] = useState<Onboarding | null | undefined>(undefined);
  const [claiming, setClaiming] = useState(false);

  const load = useCallback(() => {
    api.listOnboardings({ device_id: device.id }).then(
      (r) => setLatest(r.items[0] ?? null),
      () => setLatest(null),
    );
  }, [device.id]);
  useEffect(load, [load]);

  if (latest === undefined) return null;
  return (
    <div className="panel">
      <h3>Sale and onboarding</h3>
      {latest ? (
        <>
          <OnboardingTimeline onboarding={latest} />
          <p className="ob-hint">
            <Link to={`/onboarding/${onboardingTab(latest.state)}`}>Open in Onboarding</Link>
          </p>
        </>
      ) : (
        <div className="ob-toolbar">
          <span className="dim">No sale is recorded for this device, so it has no plan or site from onboarding.</span>
          <button className="btn" onClick={() => setClaiming(true)} disabled={!canManage(role)}>
            Claim device
          </button>
        </div>
      )}
      {claiming && (
        <OnboardingWizard
          device={device}
          onClose={() => setClaiming(false)}
          onCreated={() => {
            setClaiming(false);
            toast("Sale recorded; the device moves on at its next check-in", "success");
            load();
          }}
        />
      )}
    </div>
  );
}
