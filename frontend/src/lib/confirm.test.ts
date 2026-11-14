import { afterEach, describe, expect, it, vi } from "vitest";
import { answerConfirm, confirmDialog, describeConfirm, subscribeConfirm, type ConfirmRequest } from "./confirm";

describe("confirmDialog", () => {
  afterEach(() => vi.restoreAllMocks());

  it("names the button after the action and marks destructive ones", () => {
    expect(describeConfirm('Delete group "A"? This cannot be undone.')).toEqual({ confirmLabel: "Delete", danger: true });
    expect(describeConfirm("Factory reset this device?")).toEqual({ confirmLabel: "Factory reset", danger: true });
    expect(describeConfirm("Reboot the gateway?")).toEqual({ confirmLabel: "Reboot", danger: false });
    // A button that says "Cancel" would read as "close this dialog".
    expect(describeConfirm("Cancel the 4 jobs still queued?").confirmLabel).toBe("Yes, cancel it");
    expect(describeConfirm("Something else?")).toEqual({ confirmLabel: "Confirm", danger: false });
    expect(describeConfirm("Delete it?", { confirmLabel: "Remove forever", danger: false })).toEqual({ confirmLabel: "Remove forever", danger: false });
  });

  it("falls back to the browser's confirm when no host is mounted", async () => {
    const native = vi.spyOn(window, "confirm").mockReturnValue(true);
    expect(await confirmDialog("Delete it?")).toBe(true);
    expect(native).toHaveBeenCalledWith("Delete it?");
  });

  it("asks through the host, splits paragraphs, and resolves with the answer", async () => {
    const native = vi.spyOn(window, "confirm");
    let shown: ConfirmRequest | null = null;
    const unsubscribe = subscribeConfirm((r) => (shown = r));
    try {
      const answer = confirmDialog("Reboot X?\n\nIts backhaul drops for about ten minutes.");
      expect(shown!.paragraphs).toEqual(["Reboot X?", "Its backhaul drops for about ten minutes."]);
      // A second question while one is open is refused, not stacked.
      expect(await confirmDialog("Delete Y?")).toBe(false);
      answerConfirm(true);
      expect(await answer).toBe(true);
      expect(shown).toBeNull();
      expect(native).not.toHaveBeenCalled();
    } finally {
      unsubscribe();
    }
  });
});
