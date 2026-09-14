import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./client";

describe("capture API client", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("startDeviceCapture POSTs to the device-scoped route", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "cap-1", match_type: "device", status: "ACTIVE" }), { status: 202 }),
    );

    const res = await api.startDeviceCapture("dev-1", "CWMP");

    expect(res.id).toBe("cap-1");
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/devices/dev-1/captures"),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("startCapture POSTs match_type, match_value, and protocol", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "cap-2" }), { status: 202 }),
    );

    const res = await api.startCapture("identity", "001349+S1", "CWMP");

    expect(res.id).toBe("cap-2");
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/captures"),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ match_type: "identity", match_value: "001349+S1", protocol: "CWMP" }),
      }),
    );
  });

  it("listCaptures GETs the list route", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ items: [] }), { status: 200 }),
    );

    const res = await api.listCaptures();

    expect(res.items).toEqual([]);
  });
});
