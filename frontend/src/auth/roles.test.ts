import { describe, it, expect } from "vitest";
import { canWrite, canAdmin } from "./roles";

describe("canWrite", () => {
  it("treats a null role (auth disabled) as fully open", () => {
    expect(canWrite(null)).toBe(true);
  });

  it("gates readonly out, noc and above in", () => {
    expect(canWrite("readonly")).toBe(false);
    expect(canWrite("noc")).toBe(true);
    expect(canWrite("manager")).toBe(true);
    expect(canWrite("superadmin")).toBe(true);
  });

  it("treats an unrecognized role string as the most restrictive case, not a crash", () => {
    expect(canWrite("not-a-real-role")).toBe(false);
  });
});

describe("canAdmin", () => {
  it("treats a null role (auth disabled) as fully open", () => {
    expect(canAdmin(null)).toBe(true);
  });

  it("only superadmin clears the admin bar", () => {
    expect(canAdmin("readonly")).toBe(false);
    expect(canAdmin("noc")).toBe(false);
    expect(canAdmin("manager")).toBe(false);
    expect(canAdmin("superadmin")).toBe(true);
  });

  it("treats an unrecognized role string as the most restrictive case, not a crash", () => {
    expect(canAdmin("not-a-real-role")).toBe(false);
  });
});
