import { describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "./aeroId";
import { capabilityAvailable, parseCapabilities } from "./capabilities";
import {
  routeEligibility,
  preserveAdmittedCapabilityPage,
} from "../capabilityNavigation";
import type { CapabilityReport } from "../types";

function report(): CapabilityReport {
  return {
    schema_version: 1,
    account_id: "019f0123-4567-7000-8000-0123456789ab",
    tenant_id: "tenant-a",
    client_id: "console",
    issuer: "https://snaplink.example",
    home_region: "local",
    local_region: "local",
    generated_at: new Date().toISOString(),
    authorization_version: "unsupported",
    partial: false,
    capabilities: [
      {
        name: "account.self.read",
        registered: true,
        enabled: true,
        authorization: "allowed",
        authorization_scope: "self",
        available: true,
      },
      {
        name: "audit.read",
        registered: true,
        enabled: true,
        authorization: "denied",
        authorization_scope: "global",
        available: false,
      },
      {
        name: "callback.manage",
        registered: true,
        enabled: false,
        authorization: "allowed",
        authorization_scope: "global",
        available: false,
      },
    ],
    datasets: [
      {
        name: "aero-vault.usage",
        source: "aero-vault",
        pii: false,
        registered: true,
        enabled: true,
        authorization: "allowed",
        resource_authorization: "unknown",
        status: "fresh",
        fresh: true,
        version: 3,
      },
    ],
  };
}

describe("typed capability contract", () => {
  it("reads only the authenticated metadata endpoint with cancellation", async () => {
    const expected = report();
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL) =>
        new Response(JSON.stringify({ data: expected })),
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );
    const controller = new AbortController();
    expect(await client.getCapabilities(controller.signal)).toEqual(expected);
    expect(String(fetcher.mock.calls[0]?.[0])).toContain("/v1/me/capabilities");
    expect(capabilityAvailable(report(), ["audit.read"])).toBe(false);
  });

  it("fails closed on a malformed/unsupported/contradictory report", () => {
    for (const value of [
      {},
      { ...report(), schema_version: 2 },
      { ...report(), generated_at: "not-a-time" },
      { ...report(), authorization_version: "invented-policy-version" },
      {
        ...report(),
        capabilities: [{ ...report().capabilities[1], available: true }],
      },
      {
        ...report(),
        datasets: [
          { ...report().datasets[0], resource_authorization: "allowed" },
        ],
      },
      {
        ...report(),
        capabilities: [report().capabilities[0], report().capabilities[0]],
      },
    ]) {
      expect(() => parseCapabilities(value)).toThrow("不会推断权限");
    }
  });

  it("retains only the already-admitted exact view on refresh/outage, never a new deep link", () => {
    expect(
      preserveAdmittedCapabilityPage("jobs/id-a", "jobs/id-a", undefined),
    ).toBe(true);
    expect(
      preserveAdmittedCapabilityPage("jobs/id-b", "jobs/id-a", undefined),
    ).toBe(false);
    expect(
      preserveAdmittedCapabilityPage("admin-accounts", "jobs", undefined),
    ).toBe(false);
    expect(preserveAdmittedCapabilityPage("jobs", undefined, undefined)).toBe(
      false,
    );
    expect(preserveAdmittedCapabilityPage("jobs", "jobs", report())).toBe(
      false,
    );
    expect(routeEligibility("admin-accounts", undefined).available).toBe(false);
  });

  it("admits current callback metadata reads without borrowing eligibility for writes", () => {
    const value = report();
    value.capabilities.push({
      name: "callback.read",
      registered: true,
      enabled: true,
      authorization: "allowed",
      authorization_scope: "global",
      available: true,
    });
    expect(routeEligibility("callbacks", value).available).toBe(true);
    expect(capabilityAvailable(value, ["callback.manage"])).toBe(false);
    value.capabilities[value.capabilities.length - 1].authorization = "unknown";
    value.capabilities[value.capabilities.length - 1].available = false;
    expect(routeEligibility("callbacks", value).available).toBe(false);
  });
  it("gates administrative deep links without blocking eligible self projection reads", () => {
    expect(routeEligibility("overview", report()).available).toBe(true);
    expect(routeEligibility("audit", report()).available).toBe(false);
    expect(routeEligibility("callbacks", report()).reason).toBe("能力尚未确认");
    expect(routeEligibility("admin-accounts", undefined).available).toBe(false);
    const unknown = report();
    unknown.capabilities[1].authorization = "unknown";
    expect(routeEligibility("audit", unknown).reason).toBe("权限未知");
    expect(routeEligibility("services", undefined).available).toBe(true);
  });
});
