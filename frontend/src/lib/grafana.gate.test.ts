import { describe, expect, it, vi } from "vitest";
import { openGatedGrafana, type TabLike } from "./grafana";

function fakeTab(): TabLike & { closed: boolean } {
  const tab = { opener: {} as unknown, location: { href: "about:blank" }, closed: false, close: () => { tab.closed = true; } };
  return tab;
}

describe("openGatedGrafana", () => {
  it("opens the tab first, then sends it to the ticket URL with no opener", async () => {
    const tab = fakeTab();
    const open = vi.fn(() => tab);
    const problem = await openGatedGrafana(async () => {
      expect(open).toHaveBeenCalledTimes(1); // opened before the request: still a user gesture
      return { url: "https://grafana.example/acs-gate/enter?ticket=t" };
    }, open);
    expect(problem).toBeNull();
    expect(tab.location.href).toBe("https://grafana.example/acs-gate/enter?ticket=t");
    expect(tab.opener).toBeNull();
  });

  it("closes the tab and explains a refusal", async () => {
    const tab = fakeTab();
    const problem = await openGatedGrafana(async () => { throw Object.assign(new Error("no"), { status: 403 }); }, () => tab);
    expect(tab.closed).toBe(true);
    expect(problem).toMatch(/platform operators only/);
  });

  it("reports a blocked pop-up", async () => {
    const problem = await openGatedGrafana(async () => ({ url: "https://grafana.example/x" }), () => null);
    expect(problem).toMatch(/blocked/);
  });
});
