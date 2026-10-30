import { describe, expect, it } from "vitest";
import { groupCountsByVendor, vendorFamilyOf, vendorGroup, vendorLabel } from "./vendorFamily";

describe("vendor family", () => {
  it("treats the Beacon's ALCL manufacturer as Nokia family", () => {
    expect(vendorFamilyOf("ALCL", "Beacon 3.1")).toBe("Nokia");
    expect(vendorFamilyOf("Nokia", "3TG02241")).toBe("Nokia");
    expect(vendorFamilyOf("Alcatel-Lucent")).toBe("Nokia");
    expect(vendorFamilyOf("Acme", "FastMile 5G Gateway")).toBe("Nokia");
    expect(vendorFamilyOf("Huawei Technologies Co., Ltd.")).toBe("Huawei");
    expect(vendorFamilyOf("Zyxel")).toBe("Other");
    expect(vendorFamilyOf("Aluminium Networks")).toBe("Other");
  });

  it("groups and labels without hiding the reported string", () => {
    expect(vendorGroup("ALCL")).toBe("Nokia");
    expect(vendorGroup(" Zyxel ")).toBe("Zyxel");
    expect(vendorGroup("")).toBe("Unknown");
    expect(vendorLabel("ALCL")).toBe("Nokia (ALCL)");
    expect(vendorLabel("Nokia")).toBe("Nokia");
    expect(vendorLabel("Zyxel")).toBe("Zyxel");
  });

  it("merges per-manufacturer counts into one Nokia bucket", () => {
    expect(groupCountsByVendor({ Nokia: 3, ALCL: 2, Zyxel: 1 })).toEqual({ Nokia: 5, Zyxel: 1 });
    expect(groupCountsByVendor(undefined)).toEqual({});
  });
});
