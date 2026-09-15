import { describe, expect, it } from "vitest";
import type { Device, Job, ParameterCache } from "../api/types";
import { assessDeviceOnboarding } from "./deviceOnboarding";

function device(overrides: Partial<Device> = {}): Device {
  return {
    id: "dev-1",
    oui_serial: "A1B2C3+SERIAL1",
    manufacturer: "Huawei Technologies Co., Ltd.",
    oui: "A1B2C3",
    product_class: "N5368X",
    serial_number: "SERIAL1",
    data_model_root: "Device.",
    online_status: "ONLINE",
    connection_request_mode: "DIRECT",
    ...overrides,
  };
}

function job(overrides: Partial<Job> = {}): Job {
  return {
    command_key: "GET_PARAMETER-abc",
    device_id: "dev-1",
    type: "GET_PARAMETER",
    status: "FAILED",
    created_at: "2026-09-15T10:00:00Z",
    updated_at: "2026-09-15T10:01:00Z",
    ...overrides,
  };
}

describe("assessDeviceOnboarding", () => {
  it("recognizes Huawei N5368X and recommends preserving Digest-SHA256", () => {
    const result = assessDeviceOnboarding(device({ last_inform_at: undefined }), {}, []);

    expect(result.isHuawei).toBe(true);
    expect(result.isHuaweiN5368X).toBe(true);
    expect(result.hints.some((hint) => hint.id === "huawei-n5368x-digest")).toBe(true);
    expect(result.hints.find((hint) => hint.id === "huawei-n5368x-digest")?.detail).toContain("ACS_DIGEST_ALGORITHMS=SHA-256");
    expect(result.checks.find((check) => check.id === "inform")?.status).toBe("fail");
  });

  it("turns a 9005 into parameter-discovery guidance", () => {
    const params: ParameterCache = {
      "Device.DeviceInfo.SoftwareVersion": { value: "V1", updated_at: "2026-09-15T10:00:00Z", source: "INFORM" },
    };
    const result = assessDeviceOnboarding(
      device({ last_inform_at: "2026-09-15T10:00:00Z", cwmp_auth_mode: "DIGEST" }),
      params,
      [job({ fault_code: "9005", fault_string: "Invalid parameter name" })],
    );

    expect(result.recentFailures).toHaveLength(1);
    expect(result.hints.some((hint) => hint.title.includes("9005"))).toBe(true);
    expect(result.hints.some((hint) => hint.detail.includes("parameter discovery"))).toBe(true);
  });

  it("warns about Huawei TR-098 passphrase writability", () => {
    const result = assessDeviceOnboarding(
      device({ product_class: "HG8145V5", data_model_root: "InternetGatewayDevice." }),
      {},
      [],
    );

    expect(result.hints.some((hint) => hint.id === "huawei-tr098-wifi")).toBe(true);
    expect(result.hints.find((hint) => hint.id === "huawei-tr098-wifi")?.detail).toContain("PreSharedKey.1.KeyPassphrase");
  });
});
