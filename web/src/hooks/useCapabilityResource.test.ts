import { createApp, nextTick, watchEffect } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CapabilityReport } from "../types";
import { commandTestReport } from "../components/commandTestFixtures";
import { useCapabilityResource } from "./useCapabilityResource";
const apps: ReturnType<typeof createApp>[] = [];
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-07T12:00:00Z"));
});
afterEach(() => {
  apps.splice(0).forEach((app) => app.unmount());
  vi.restoreAllMocks();
  vi.useRealTimers();
});
async function flush() {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
    await nextTick();
  }
}
function mount() {
  const getCapabilities = vi.fn(async (_signal?: AbortSignal) =>
    commandTestReport(),
  );
  let resource!: ReturnType<typeof useCapabilityResource>;
  const app = createApp({
    setup() {
      resource = useCapabilityResource(
        { getCapabilities } as unknown as AeroIdClient,
        "https://snaplink.example",
        "console",
      );
      return () => null;
    },
  });
  apps.push(app);
  app.mount(document.createElement("div"));
  return { app, getCapabilities, resource };
}
describe("current capability metadata refresh", () => {
  it("coalesces refresh, invalidates old grants while checking, and keeps them unavailable on outage", async () => {
    const item = mount();
    await flush();
    expect(item.resource.report()).toBeDefined();
    let fail!: (error: Error) => void;
    item.getCapabilities.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          fail = reject;
        }),
    );
    const first = item.resource.refresh();
    const second = item.resource.refresh();
    expect(first).toBe(second);
    expect(item.resource.report()).toBeUndefined();
    expect(item.resource.resource.data).toBeDefined(); // Retain view, not a grant.
    expect(item.getCapabilities).toHaveBeenCalledTimes(2);
    fail(new Error("authority unavailable"));
    await first;
    await flush();
    expect(item.resource.report()).toBeUndefined();
    expect(item.resource.resource.error?.message).toBe("authority unavailable");
  });
  it.each(["allowed", "denied", "outage", "wrong client"])(
    "never republishes the old grant while a %s refresh settles",
    async (outcome) => {
      const item = mount();
      await flush();
      const original = item.resource.report();
      const replacement = commandTestReport();
      if (outcome === "denied")
        replacement.capabilities = replacement.capabilities.map(
          (capability) => ({
            ...capability,
            authorization: "denied",
            available: false,
          }),
        );
      if (outcome === "wrong client") replacement.client_id = "other-console";
      if (outcome === "outage")
        item.getCapabilities.mockRejectedValue(
          new Error("authority unavailable"),
        );
      else item.getCapabilities.mockResolvedValue(replacement);
      const observed: (CapabilityReport | undefined)[] = [];
      const stop = watchEffect(
        () => {
          observed.push(item.resource.report());
        },
        { flush: "sync" },
      );
      observed.length = 0;
      try {
        await item.resource.refresh();
        await flush();
        expect(observed).not.toContain(original);
        expect(item.resource.report()).toBe(
          outcome === "denied" || outcome === "allowed"
            ? replacement
            : undefined,
        );
      } finally {
        stop();
      }
    },
  );
  it("refreshes on focus/visible pacing only, never auto-posting or overlapping GETs", async () => {
    const item = mount();
    await flush();
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(60000);
    expect(item.getCapabilities).toHaveBeenCalledOnce();
    hidden.mockReturnValue(false);
    document.dispatchEvent(new Event("visibilitychange"));
    await flush();
    expect(item.getCapabilities).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(60000);
    await flush();
    expect(item.getCapabilities).toHaveBeenCalledTimes(3);
  });
  it("rejects another issuer/client and does not revive the previous report", async () => {
    const item = mount();
    await flush();
    item.getCapabilities.mockResolvedValue({
      ...commandTestReport(),
      client_id: "another-console",
    });
    await item.resource.refresh();
    expect(item.resource.report()).toBeUndefined();
    expect(item.resource.resource.error?.message).toContain(
      "当前登录客户端不一致",
    );
  });
  it("has a local two-minute guidance budget, not a policy version", async () => {
    const item = mount();
    await flush();
    vi.setSystemTime(new Date(Date.now() + 120001));
    expect(item.resource.report()).toBeUndefined();
    expect(item.getCapabilities).toHaveBeenCalledOnce();
  });
  it("cancels in-flight metadata and removes timers/listeners on unmount", async () => {
    const item = mount();
    await flush();
    let finish!: (value: CapabilityReport) => void;
    item.getCapabilities.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const request = item.resource.refresh();
    const signal = item.getCapabilities.mock.calls[1][0]!;
    item.app.unmount();
    expect(signal.aborted).toBe(true);
    finish(commandTestReport());
    await request;
    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(180000);
    expect(item.getCapabilities).toHaveBeenCalledTimes(2);
  });
});
