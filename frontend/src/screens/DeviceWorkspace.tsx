// /devices/<id>/<section>: one device's workspace, with a URL per sub-page
// (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md §2).
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { parseDevicePath } from "../lib/deviceSections";
import { DeviceDetail } from "./DeviceDetail";

export function DeviceWorkspace() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const target = parseDevicePath(pathname);
  if (!target) return <Navigate to="/fleet" replace />;
  // Keyed by device so moving to another device starts from clean state;
  // switching sub-pages keeps the same instance.
  return <DeviceDetail key={target.id} id={target.id} section={target.section} onClose={() => navigate("/fleet")} />;
}
