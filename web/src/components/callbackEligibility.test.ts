import { describe, expect, it } from "vitest";
import {
  callbackActionAvailable,
  callbackReadAvailable,
  callbackReplayAvailable,
} from "./callbackEligibility";
import {
  callbackTestDelivery,
  callbackTestReport,
} from "./callbackTestFixtures";
import { testScope } from "./commandTestFixtures";

describe("current callback action guidance", () => {
  it("does not lend read eligibility to writes or require a delivery key for metadata", () => {
    const report = callbackTestReport();
    const write = report.capabilities.find(
      (item) => item.name === "callback.manage",
    )!;
    write.enabled = false;
    write.available = false;
    expect(callbackReadAvailable(report, testScope)).toBe(true);
    expect(callbackActionAvailable(report, testScope)).toBe(false);
    expect(
      callbackReplayAvailable(callbackTestDelivery(), report, testScope),
    ).toBe(false);
  });
  it("requires complete scope, verified current global guidance and all eligibility flags", () => {
    expect(callbackActionAvailable(undefined, testScope)).toBe(false);
    expect(callbackReadAvailable(callbackTestReport(), undefined)).toBe(false);
    for (const mutation of [
      { tenant_id: "tenant-b" },
      { issuer: "another-issuer" },
      { client_id: "another-client" },
      { account_id: "01800000-0000-7000-8000-000000000099" },
    ])
      expect(
        callbackActionAvailable(
          { ...callbackTestReport(), ...mutation },
          testScope,
        ),
      ).toBe(false);
    for (const mutation of [
      { authorization: "unknown" as const },
      { authorization: "denied" as const },
      { authorization_scope: "self" as const },
      { registered: false },
      { enabled: false },
      { available: false },
    ]) {
      const report = callbackTestReport();
      Object.assign(
        report.capabilities.find((item) => item.name === "callback.manage")!,
        mutation,
      );
      expect(callbackActionAvailable(report, testScope)).toBe(false);
    }
  });
  it("does not treat success, pending, delivering, expiry or foreign tenant as a replay grant", () => {
    const report = callbackTestReport();
    const item = callbackTestDelivery();
    expect(callbackReplayAvailable(item, report, testScope)).toBe(true);
    for (const status of [
      "pending",
      "delivering",
      "success",
      "unknown",
    ] as const)
      expect(
        callbackReplayAvailable(
          {
            ...item,
            status,
            lease_until: new Date(Date.now() - 1000).toISOString(),
          },
          report,
          testScope,
        ),
      ).toBe(false);
    expect(
      callbackReplayAvailable(
        { ...item, tenant_id: "tenant-b" },
        report,
        testScope,
      ),
    ).toBe(false);
    expect(
      callbackReplayAvailable(
        { ...item, lease_until: new Date(Date.now() - 1000).toISOString() },
        report,
        testScope,
      ),
    ).toBe(false);
    expect(
      callbackReplayAvailable({ ...item, revision: 0 }, report, testScope),
    ).toBe(false);
    expect(
      callbackReplayAvailable(
        { ...item, lease_until: new Date(Date.now() + 60000).toISOString() },
        report,
        testScope,
      ),
    ).toBe(false);
  });
});
