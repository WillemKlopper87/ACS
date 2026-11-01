import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { SubNav } from "./SubNav";

afterEach(cleanup);

describe("SubNav", () => {
  it("links every item, marks the current page and groups the advanced ones", () => {
    render(
      <MemoryRouter>
        <SubNav
          label="Device sections"
          current="wifi"
          items={[
            { id: "overview", label: "Overview", to: "/devices/gw-1/overview" },
            { id: "wifi", label: "Wi-Fi", to: "/devices/gw-1/wifi" },
            { id: "parameters", label: "Parameters", to: "/devices/gw-1/parameters", group: "Advanced" },
          ]}
        />
      </MemoryRouter>,
    );
    const nav = screen.getByRole("navigation", { name: "Device sections" });
    const wifi = within(nav).getByRole("link", { name: "Wi-Fi" });
    expect(wifi.getAttribute("href")).toBe("/devices/gw-1/wifi");
    expect(wifi.getAttribute("aria-current")).toBe("page");
    expect(within(nav).getByRole("link", { name: "Overview" }).getAttribute("aria-current")).toBeNull();
    expect(within(nav).getByText("Advanced")).toBeTruthy();
    // The group label precedes its items.
    const order = Array.from(nav.querySelectorAll("li")).map((li) => li.textContent);
    expect(order).toEqual(["Overview", "Wi-Fi", "Advanced", "Parameters"]);
  });
});
