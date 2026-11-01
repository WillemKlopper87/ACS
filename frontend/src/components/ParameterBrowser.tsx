import { useMemo, useState, type ReactNode } from "react";
import type { CachedParameter, ParameterCache } from "../api/types";
import { timeAgo } from "../lib/format";

// Integer-range extremes CPEs report when a metric has no current value
// (Nokia FastMile LTE stats while on 5G: -32768 / 4294967295; TR-181
// 2147483647). Mirrors adapters.IsCellularSentinel in the backend.
const SENTINELS = new Set(["-32768", "32767", "-2147483648", "2147483647", "4294967295"]);
const SEARCH_LIMIT = 300;
const RECENT_MS = 60 * 60 * 1000;

interface TreeNode {
  name: string;
  path: string; // object path ending in "." for branches, full path for leaves
  children: Map<string, TreeNode>;
  leaf?: CachedParameter;
  leafCount: number;
}

function buildTree(entries: [string, CachedParameter][]): TreeNode {
  const root: TreeNode = { name: "", path: "", children: new Map(), leafCount: 0 };
  for (const [path, value] of entries) {
    const segments = path.split(".");
    let node = root;
    node.leafCount++;
    for (let i = 0; i < segments.length; i++) {
      const isLeaf = i === segments.length - 1;
      const key = segments[i];
      let child = node.children.get(key);
      if (!child) {
        child = { name: key, path: isLeaf ? path : segments.slice(0, i + 1).join(".") + ".", children: new Map(), leafCount: 0 };
        node.children.set(key, child);
      }
      if (isLeaf) child.leaf = value;
      else child.leafCount++;
      node = child;
    }
  }
  return root;
}

// Numeric instance segments sort numerically (SSID.2 before SSID.10).
function compareNames(a: string, b: string): number {
  const na = Number(a);
  const nb = Number(b);
  if (!Number.isNaN(na) && !Number.isNaN(nb)) return na - nb;
  return a.localeCompare(b);
}

interface Props {
  params: ParameterCache;
  /** Discovered names → writable, when discovery has run. */
  writable?: Record<string, boolean> | null;
  /** Path whose detail (value history) is open. */
  selectedPath: string | null;
  onSelect: (path: string) => void;
  renderDetail: (path: string) => ReactNode;
}

/**
 * Parameter cache as a collapsible tree with search and filters. A large CPE
 * (the Nokia FastMile caches 2,000+ parameters) made the previous flat list
 * unusable; only expanded branches are rendered, so the 5-second live
 * refresh stays cheap and expansion state survives it.
 */
export function ParameterBrowser({ params, writable, selectedPath, onSelect, renderDetail }: Props) {
  const [query, setQuery] = useState("");
  const [onlyWritable, setOnlyWritable] = useState(false);
  const [onlyRecent, setOnlyRecent] = useState(false);
  const [onlyVendor, setOnlyVendor] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(["Device.", "InternetGatewayDevice."]));

  const total = Object.keys(params).length;
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const now = Date.now();
    return Object.entries(params)
      .filter(([path, v]) => {
        if (onlyWritable && !writable?.[path]) return false;
        if (onlyVendor && !/\.X_/.test(path)) return false;
        if (onlyRecent && !(now - new Date(v.updated_at).getTime() < RECENT_MS)) return false;
        if (q && !path.toLowerCase().includes(q) && !String(v.value).toLowerCase().includes(q)) return false;
        return true;
      })
      .sort(([a], [b]) => a.localeCompare(b));
  }, [params, query, onlyWritable, onlyRecent, onlyVendor, writable]);

  const searching = query.trim() !== "";
  const tree = useMemo(() => (searching ? null : buildTree(filtered)), [filtered, searching]);

  const toggle = (path: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });

  const leafRow = (path: string, v: CachedParameter, label: string, depth: number) => {
    const noValue = SENTINELS.has(String(v.value).trim());
    return (
      <div key={path}>
        <div
          className="param-row"
          style={{ cursor: "pointer", paddingLeft: `${depth * 0.9}rem` }}
          onClick={() => onSelect(path)}
          title={`${path} — click to view value history`}
        >
          <span className="path">
            {label}
            {writable?.[path] && <span className="pill pill-ok" style={{ marginLeft: "0.4rem", fontSize: "0.65rem" }}>RW</span>}
          </span>
          <span className="val" style={noValue ? { color: "var(--ink-faint)", fontStyle: "italic" } : undefined}>
            {noValue ? `no value (${v.value})` : v.value}
          </span>
          <span className="src">
            {v.source} · {timeAgo(v.updated_at)} · {selectedPath === path ? "hide history ▲" : "history ▼"}
          </span>
        </div>
        {selectedPath === path && renderDetail(path)}
      </div>
    );
  };

  const renderNode = (node: TreeNode, depth: number): ReactNode[] =>
    [...node.children.values()]
      .sort((a, b) => {
        // Branches before leaves, then by name.
        if (!!a.leaf !== !!b.leaf) return a.leaf ? 1 : -1;
        return compareNames(a.name, b.name);
      })
      .flatMap((child) => {
        if (child.leaf && child.children.size === 0) return [leafRow(child.path, child.leaf, child.name, depth)];
        const open = expanded.has(child.path);
        return [
          <div
            key={child.path}
            className="param-row"
            role="button"
            aria-expanded={open}
            style={{ cursor: "pointer", paddingLeft: `${depth * 0.9}rem`, fontWeight: 600 }}
            onClick={() => toggle(child.path)}
          >
            <span className="path">
              {open ? "▾" : "▸"} {child.name}
            </span>
            <span className="src">{child.leafCount.toLocaleString()} parameters</span>
          </div>,
          ...(open ? renderNode(child, depth + 1) : []),
        ];
      });

  const filterLabel = (checked: boolean, set: (v: boolean) => void, text: string, disabled = false) => (
    <label style={{ fontSize: "0.8rem", display: "inline-flex", alignItems: "center", gap: "0.3rem", opacity: disabled ? 0.5 : 1 }}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => set(e.target.checked)} />
      {text}
    </label>
  );

  return (
    <div>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem", alignItems: "center", margin: "0.4rem 0 0.6rem" }}>
        <input
          aria-label="Search parameters"
          placeholder={`Search ${total.toLocaleString()} parameters by path or value…`}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ flex: "1 1 18rem" }}
        />
        {filterLabel(onlyWritable, setOnlyWritable, "Writable", !writable)}
        {filterLabel(onlyRecent, setOnlyRecent, "Changed < 1 h")}
        {filterLabel(onlyVendor, setOnlyVendor, "Vendor (X_)")}
        {!searching && (
          <button className="btn" type="button" onClick={() => setExpanded(new Set())}>
            Collapse all
          </button>
        )}
      </div>
      <div className="dim" style={{ fontSize: "0.76rem", marginBottom: "0.4rem" }}>
        {filtered.length === total ? `${total.toLocaleString()} parameters` : `${filtered.length.toLocaleString()} of ${total.toLocaleString()} parameters match`}
      </div>
      {filtered.length === 0 ? (
        <p className="dim" style={{ fontSize: "0.84rem", margin: 0 }}>No parameters match.</p>
      ) : searching ? (
        <>
          {filtered.slice(0, SEARCH_LIMIT).map(([path, v]) => leafRow(path, v, path, 0))}
          {filtered.length > SEARCH_LIMIT && (
            <p className="dim" style={{ fontSize: "0.76rem" }}>
              Showing the first {SEARCH_LIMIT} of {filtered.length.toLocaleString()} — refine the search.
            </p>
          )}
        </>
      ) : (
        tree && renderNode(tree, 0)
      )}
    </div>
  );
}
