import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { WebhookRow } from "./BSSIntegration";
import type { BSSWebhookSubscription } from "../api/types";

// Regression: the BSS Integration screen crashed with "Cannot read
// properties of undefined (reading 'length')" as soon as a single webhook
// subscription existed.
//
// The cause was a contract drift rather than a rendering mistake. Audit H-7
// removed the HMAC secret from every API response — bss-integration-guide.md
// promises it is "never returned by any endpoint after creation" — but the
// frontend type kept declaring `secret: string` as required, so TypeScript
// raised nothing while the value arrived undefined and maskSecret() read
// .length off it.
//
// This test pins the real API shape: a subscription with no secret field.
describe("WebhookRow", () => {
  const subscription = {
    id: "ef9c3e37-5b68-47a6-b54a-65a4dae6152b",
    account_id: "ACCT-1001",
    target_url: "https://bss.example.net/acs-events",
    event_types: ["job.completed", "job.failed"],
    created_at: "2026-09-06T18:26:34Z",
  } satisfies BSSWebhookSubscription;

  it("renders a subscription as the API actually returns it — with no secret", () => {
    const { container } = render(<WebhookRow subscription={subscription} onDelete={() => {}} />);
    expect(container.textContent).toContain("ACCT-1001");
    expect(container.textContent).toContain("https://bss.example.net/acs-events");
    expect(container.textContent).toContain("job.completed, job.failed");
  });

  it("never claims to display the secret, which the server does not return", () => {
    const { container } = render(<WebhookRow subscription={subscription} onDelete={() => {}} />);
    expect(container.textContent?.toLowerCase()).not.toContain("secret");
  });

  it("renders a fleet-wide subscription, where account_id is null", () => {
    const { container } = render(
      <WebhookRow subscription={{ ...subscription, account_id: null }} onDelete={() => {}} />,
    );
    expect(container.textContent).toContain("fleet-wide");
  });
});
