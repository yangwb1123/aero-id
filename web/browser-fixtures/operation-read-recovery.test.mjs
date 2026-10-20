import { describe, expect, it, vi } from "vitest";
import { checkOperationReadRecovery } from "./operation-read-recovery.mjs";
import { operationID } from "./operation-results-data.mjs";

// Deterministic browser-driver sequencing model; not a browser/auth test.
function driver(status, { extraRefreshRead = false } = {}) {
  const activeStatuses = new Set([
    "requested",
    "running",
    "waiting",
    "compensating",
  ]);
  const events = [];
  const requests = [];
  let view = "detail";
  let scenario = "operation-requested";
  const read = () =>
    requests.push({ path: `/v1/operations/${operationID}`, method: "GET" });
  const load = vi.fn(async (name) => {
    events.push(`load:${name}`);
    expect(name).toBe("operation-partial");
    status = "partially_completed";
    view = "detail";
    read();
  });
  const setScenario = vi.fn((name) => {
    events.push(`scenario:${name}`);
    scenario = name;
    // Exercise the adverse interleaving, without sleeping or changing budgets:
    // an already eligible automatic GET receives the newly armed fault first.
    if (name === "operation-read-failure" && activeStatuses.has(status)) {
      read();
      view = "error";
    }
  });
  const clicks = vi.fn(async (name, options) => {
    expect(options).toBeUndefined();
    events.push(`click:${name}`);
    if (name === "刷新状态") {
      if (view !== "detail")
        throw new Error("refresh button detached by background metadata GET");
      expect(scenario).toBe("operation-read-failure");
      read();
      if (extraRefreshRead) read();
      view = "error";
    } else {
      expect(name).toBe("重试");
      expect(view).toBe("error");
      expect(scenario).toBe("operation-partial");
      read();
      view = "detail";
    }
  });
  const page = {
    getByRole(role, { name, exact }) {
      expect(role).toBe("button");
      expect(exact).toBe(true);
      return {
        click: (options) => clicks(name, options),
        waitFor: async () => {
          expect(name).toBe("重试");
          expect(view).toBe("error");
        },
      };
    },
  };
  const panel = {
    count: async () => (view === "detail" ? 1 : 0),
    waitFor: async () => expect(view).toBe("detail"),
  };
  return { page, panel, requests, setScenario, load, events, clicks };
}

describe("Operation browser manual-read recovery scenario isolation", () => {
  it.each([
    "requested",
    "running",
    "waiting",
    "compensating",
    "unknown",
    "partially_completed",
  ])(
    "does not arm a manual-read fault against inherited %s polling",
    async (status) => {
      const state = driver(status);
      await checkOperationReadRecovery(state);
      expect(state.load).toHaveBeenCalledExactlyOnceWith("operation-partial");
      expect(state.events).toEqual([
        "load:operation-partial",
        "scenario:operation-read-failure",
        "click:刷新状态",
        "scenario:operation-partial",
        "click:重试",
      ]);
      expect(state.requests).toEqual(
        Array.from({ length: 3 }, () => ({
          path: `/v1/operations/${operationID}`,
          method: "GET",
        })),
      );
      expect(state.clicks).toHaveBeenCalledTimes(2);
    },
  );

  it("retains the one-original-GET refresh assertion", async () => {
    const state = driver("partially_completed", { extraRefreshRead: true });
    await expect(checkOperationReadRecovery(state)).rejects.toThrow(
      "explicit refresh only reads the original receipt",
    );
  });

  it("does not arm a fault or click after the independent baseline failed", async () => {
    const state = driver("requested");
    state.load.mockRejectedValue(new Error("baseline unavailable"));
    await expect(checkOperationReadRecovery(state)).rejects.toThrow(
      "baseline unavailable",
    );
    expect(state.setScenario).not.toHaveBeenCalled();
    expect(state.clicks).not.toHaveBeenCalled();
  });
});
