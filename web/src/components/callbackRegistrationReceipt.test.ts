import { describe, expect, it } from "vitest";
import { callbackRegistrationMatches } from "./callbackRegistrationReceipt";
import { callbackSubscriptionID } from "./callbackTestFixtures";
import { testScope } from "./commandTestFixtures";
const input = {
  endpoint: "https://callback.example/events",
  eventTypes: ["account.provisioned", "export.ready"],
};
const subscription = {
  id: callbackSubscriptionID,
  tenant_id: "tenant-a",
  endpoint: input.endpoint,
  event_types: ["export.ready", "account.provisioned"],
  verified: true,
  enabled: true,
};
describe("original callback registration receipt", () => {
  it("accepts the exact original plan/tenant while treating event types as a set", () => {
    expect(callbackRegistrationMatches(subscription, input, testScope)).toBe(
      true,
    );
    expect(callbackRegistrationMatches(subscription, input, undefined)).toBe(
      false,
    );
  });
  it("rejects missing/changed scope or malformed metadata without inferring success", () => {
    for (const mutation of [
      { id: "not-a-uuid" },
      { id: [callbackSubscriptionID] },
      { tenant_id: "tenant-b" },
      { endpoint: "https://another.example/events" },
      { event_types: ["account.provisioned"] },
      { event_types: ["export.ready", "export.ready"] },
      { event_types: [1] },
      { enabled: false },
      { verified: false },
    ])
      expect(
        callbackRegistrationMatches(
          { ...subscription, ...mutation },
          input,
          testScope,
        ),
      ).toBe(false);
    expect(
      callbackRegistrationMatches(
        { id: callbackSubscriptionID },
        input,
        testScope,
      ),
    ).toBe(false);
  });
});
