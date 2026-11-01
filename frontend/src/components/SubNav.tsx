// Sub-navigation inside a screen (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md
// §2-3): real links, so every sub-page has its own URL, works with the back
// button and can be shared. Vertical beside the content on wide screens,
// a scrolling row on narrow ones (index.css .subnav).
import { Link } from "react-router-dom";

export interface SubNavItem {
  id: string;
  label: string;
  to: string;
  /** Items with a group are listed after a heading, e.g. "Advanced". */
  group?: string;
}

export function SubNav({ label, items, current }: { label: string; items: SubNavItem[]; current: string }) {
  let lastGroup: string | undefined;
  return (
    <nav className="subnav" aria-label={label}>
      <ul>
        {items.flatMap((item) => {
          const rows = [];
          if (item.group && item.group !== lastGroup) {
            rows.push(
              <li key={`group-${item.group}`} className="subnav-group" aria-hidden="true">
                {item.group}
              </li>,
            );
          }
          lastGroup = item.group;
          rows.push(
            <li key={item.id}>
              <Link to={item.to} className={item.id === current ? "on" : ""} aria-current={item.id === current ? "page" : undefined}>
                {item.label}
              </Link>
            </li>,
          );
          return rows;
        })}
      </ul>
    </nav>
  );
}
