import { describe, expect, it } from "vitest";
import type { BulkOperation, BulkOperationItem } from "../api/types";
import { itemStatus, progressText, sortItems } from "./bulkOperations";
import { canCancelJob, canRetryJob } from "./jobActions";

const item = (over: Partial<BulkOperationItem>): BulkOperationItem => ({ device_id: "d", retries: 0, ...over });

describe("bulk operation items", () => {
  it("names the outcome of an item with and without a job", () => {
    expect(itemStatus(item({ job_status: "SUCCESS", command_key: "k" }))).toBe("SUCCESS");
    expect(itemStatus(item({ error: "not found" }))).toBe("NOT_QUEUED");
    expect(itemStatus(item({ command_key: "k" }))).toBe("UNKNOWN"); // job pruned
  });

  it("lists failures before work in flight before successes", () => {
    const sorted = sortItems([
      item({ device_id: "ok", job_status: "SUCCESS" }),
      item({ device_id: "sent", job_status: "RPC_SENT" }),
      item({ device_id: "bad", job_status: "FAILED" }),
      item({ device_id: "none", error: "not found" }),
    ]);
    expect(sorted.map((i) => i.device_id)).toEqual(["bad", "none", "sent", "ok"]);
  });

  it("counts every ended item as finished", () => {
    const op = { total: 10, succeeded: 3, failed: 1, cancelled: 2, not_queued: 1, unknown: 0, queued: 2, running: 1 } as BulkOperation;
    expect(progressText(op)).toBe("7 of 10 finished");
  });
});

describe("job actions", () => {
  it("offers cancel only while nothing has been sent", () => {
    expect(canCancelJob("QUEUED")).toBe(true);
    expect(canCancelJob("RPC_SENT")).toBe(false);
    expect(canCancelJob("SUCCESS")).toBe(false);
  });

  it("offers retry for an unsuccessful job of a repeatable type", () => {
    expect(canRetryJob({ status: "FAILED", type: "SET_PARAMETER" })).toBe(true);
    expect(canRetryJob({ status: "CANCELLED", type: "REBOOT" })).toBe(true);
    expect(canRetryJob({ status: "SUCCESS", type: "REBOOT" })).toBe(false);
    expect(canRetryJob({ status: "FAILED", type: "FIRMWARE_DOWNLOAD" })).toBe(false);
    expect(canRetryJob({ status: "FAILED", type: "FACTORY_RESET" })).toBe(false);
  });
});
