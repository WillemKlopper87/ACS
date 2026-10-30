import { describe, expect, it } from "vitest";
import { grafanaLink } from "./grafana";

describe("grafanaLink", () => {
  it("links straight to a published or proxied Grafana", () => {
    expect(grafanaLink("http://13.247.225.68:3000", "http://13.247.225.68:8080")).toEqual({
      href: "http://13.247.225.68:3000/",
      tunnelHint: null,
    });
    expect(grafanaLink("https://grafana.example.net/", undefined)).toEqual({ href: "https://grafana.example.net/", tunnelHint: null });
  });

  it("explains the SSH tunnel when Grafana is only on the server's loopback", () => {
    expect(grafanaLink("http://localhost:3000", "http://13.247.225.68:8080")).toEqual({
      href: "http://localhost:3000/",
      tunnelHint: "ssh -L 3000:127.0.0.1:3000 ubuntu@13.247.225.68",
    });
    expect(grafanaLink("http://127.0.0.1:3000", undefined)?.tunnelHint).toBe("ssh -L 3000:127.0.0.1:3000 ubuntu@<ACS host>");
  });

  it("shows no link when Grafana is not configured or the address is not http(s)", () => {
    expect(grafanaLink(undefined, undefined)).toBeNull();
    expect(grafanaLink("", undefined)).toBeNull();
    expect(grafanaLink("javascript:alert(1)", undefined)).toBeNull();
    expect(grafanaLink("grafana:3000", undefined)).toBeNull();
  });
});
