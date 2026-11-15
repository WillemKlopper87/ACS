import { useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { api, ApiError, type SubscriberHit } from "../api/client";
import { devicePath } from "../lib/deviceSections";
import { LookupHit } from "./SubscriberLookup";

// Find a device from any screen: by subscriber name, address, account
// number, device label or the last digits of a serial. It is the same
// fleet-wide lookup Inventory has (GET /api/v1/lookup), so it searches
// every device the operator may see, not the page that happens to be
// loaded. It runs on Enter, not per keystroke, because lookups by name
// and address are audited. Ctrl+K (or Cmd+K) puts the cursor in it.
export function GlobalSearch() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<SubscriberHit[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);

  const close = () => {
    setHits(null);
    setError(null);
  };

  // Results belong to the page they were asked from.
  useEffect(close, [pathname]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        inputRef.current?.focus();
        inputRef.current?.select();
      }
    };
    const onClick = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) close();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onClick);
    };
  }, []);

  const run = async () => {
    const query = q.trim();
    if (!query) return close();
    setBusy(true);
    setError(null);
    try {
      setHits((await api.lookupSubscribers(query, false)).items);
    } catch (err) {
      setHits([]);
      setError(err instanceof ApiError ? err.message : "Search failed");
    } finally {
      setBusy(false);
    }
  };

  const open = hits !== null;
  return (
    <div className="global-search" ref={boxRef}>
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          void run();
        }}
      >
        <input
          ref={inputRef}
          type="search"
          aria-label="Search for a device or subscriber"
          placeholder="Find a device or subscriber…  Ctrl K"
          value={q}
          maxLength={100}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              close();
              inputRef.current?.blur();
            }
          }}
        />
      </form>
      {open && (
        <div id="global-search-results" className="global-search-results" role="list" aria-label="Search results" aria-busy={busy}>
          {error ? (
            <p className="banner error" style={{ margin: 0 }}>{error}</p>
          ) : hits.length === 0 ? (
            <p className="dim global-search-note">Nothing matches. Try fewer words, the account number, or the last 4 digits of the serial.</p>
          ) : (
            <>
              <p className="dim global-search-note">
                {hits.length} match{hits.length === 1 ? "" : "es"}
                {hits.length >= 50 ? " (the first 50; add a word to narrow it)" : ""}
              </p>
              {hits.map((h) => (
                <LookupHit
                  key={h.device_id}
                  hit={h}
                  onOpen={() => {
                    close();
                    setQ("");
                    navigate(devicePath(h.device_id));
                  }}
                />
              ))}
            </>
          )}
        </div>
      )}
    </div>
  );
}
