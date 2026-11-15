import { useEffect, useState } from "react";
import { api, type SystemInfo } from "../api/client";

// How the ACS is being run, asked once per sign-in. Until the answer
// arrives, and if the API is too old to give one, the console assumes the
// NaaS integration is on, which is what every deployment was before the
// switch existed: nothing is hidden on a guess.
export function useSystemInfo(signedIn: boolean): SystemInfo | null {
  const [info, setInfo] = useState<SystemInfo | null>(null);
  useEffect(() => {
    if (!signedIn) {
      setInfo(null);
      return;
    }
    let cancelled = false;
    api
      .getSystemInfo()
      .then((i) => !cancelled && setInfo(i))
      .catch(() => !cancelled && setInfo(null));
    return () => {
      cancelled = true;
    };
  }, [signedIn]);
  return info;
}
